<?php
defined('BASEPATH') OR exit('No direct script access allowed');

/**
 * Maintenance jobs: ingest, backup, export.
 *
 * Preferred way to run them (no HTTP involved):
 *     php index.php cron daily|weekly|backup|csv
 *     php index.php cron refresh <from_id>
 *
 * Over HTTP every method requires the `X-Cron-Token` header to equal the
 * CRON_TOKEN environment variable. If CRON_TOKEN is not set, HTTP access is
 * refused completely. `refresh` is CLI-only.
 *
 * Each job is throttled by a timestamp file in application/cache/ and guarded
 * by a lock so two runs can never overlap.
 */
class Cron extends CI_Controller {

	/** @var resource|false lock handle of the running job (released when the request ends) */
	private $lock = FALSE;

	public function __construct()
	{
		parent::__construct();

		if ( ! is_cli())
		{
			$expected = (string) m3_env('CRON_TOKEN', '');
			$given = (string) $this->input->get_request_header('X-Cron-Token');

			if ($expected === '' || ! hash_equals($expected, $given))
			{
				show_404(); // do not reveal that these endpoints exist
			}
		}
	}

	public function index()
	{
		show_404();
	}

	public function daily()
	{
		if ( ! $this->_begin('daily', MINIMUM_INTERVAL_DAILY))
		{
			return;
		}

		$this->load->model('m3');
		$this->load->helper('curl');

		list($new1, $err1) = $this->_ingest('daily-program', M3_DAILY_PROGRAM_URL, function ($data) {
			if ( ! is_array($data) || ! isset($data['program']) || ! is_array($data['program']))
			{
				throw new UnexpectedValueException('Unexpected daily_program response shape');
			}
			return $this->m3->parse_programs($data['program']);
		});

		list($new2, $err2) = $this->_ingest('program-guide', M3_PROGRAM_GUIDE_URL, function ($data) {
			if ( ! is_array($data) || ! isset($data['program_guides']) || ! is_array($data['program_guides']))
			{
				throw new UnexpectedValueException('Unexpected program_guide response shape');
			}
			return $this->m3->parse_guides($data['program_guides']);
		});

		$errors = array_filter(array($err1, $err2));
		if (count($errors) === 0)
		{
			$this->_stamp('daily', time()); // only a fully successful run starts the throttle window
			log_message('debug', 'DAILY CRON: '.$new1.' + '.$new2.' new items');
			$this->_end($new1.' + '.$new2);
		}
		else
		{
			$this->_end('ERROR: '.implode('; ', $errors).' (new: '.$new1.' + '.$new2.')', FALSE);
		}
	}

	public function weekly()
	{
		set_time_limit(300);

		if ( ! $this->_begin('weekly', MINIMUM_INTERVAL_WEEKLY))
		{
			return;
		}

		$this->load->model('m3');
		$this->load->helper(array('curl', 'parser'));

		try
		{
			$res = scrape_json(M3_WEEKLY_URL, $raw);
			if ( ! is_array($res))
			{
				throw new UnexpectedValueException('Unexpected get-open response shape');
			}
			$coll_list = array_filter(array_column($res, 'id'), 'is_string');
		}
		catch (Throwable $error)
		{
			$this->_save_failed_response('weekly', isset($raw) ? $raw : '');
			log_message('error', 'WEEKLY CRON: collection list failed: '.$error->getMessage());
			$this->_end('ERROR: collection list failed: '.$error->getMessage(), FALSE);
			return;
		}

		$errors = 0;
		$coll_list_items = array();
		foreach ($coll_list as $coll)
		{
			$raw = '';
			try
			{
				$res = scrape_json(M3_COLLECTION_INFO.rawurlencode($coll), $raw);
				if (is_array($res) && isset($res['docs']) && is_array($res['docs']))
				{
					$coll_list_items = array_merge($coll_list_items, $res['docs']);
				}
			}
			catch (Throwable $error)
			{
				$errors++;
				$this->_save_failed_response('coll-'.$coll, $raw);
				log_message('error', 'WEEKLY CRON: collection '.$coll.' failed: '.$error->getMessage());
			}
		}

		if (count($coll_list_items) === 0)
		{
			log_message('error', 'WEEKLY CRON: no collection items could be fetched');
			$this->_end('ERROR: no collection items could be fetched (errors: '.$errors.')', FALSE);
			return;
		}

		$new_items = $this->_parse_genre_list_items($coll_list_items, $errors);
		$new = $this->m3->insert_ignore_programs($this->m3->parse_programs($new_items));

		$this->_stamp('weekly', time());
		log_message('debug', 'WEEKLY CRON: '.$new.' new items, '.$errors.' errors');

		$this->_end($new.($errors ? ' (errors: '.$errors.')' : ''));
	}

	/**
	 * Collects the program ids of the collection items (expanding series through their HTML pages)
	 * and fetches the details of the ones that are not stored yet.
	 *
	 * @param array $items collection docs
	 * @param int   $errors incremented for every failed upstream request
	 * @return array decoded upstream items
	 */
	private function _parse_genre_list_items($items, &$errors)
	{
		$program_ids = array();
		foreach ($items as $item)
		{
			if ( ! is_array($item))
			{
				continue;
			}

			if ( ! empty($item['seriesId']) && is_string($item['seriesId']))
			{
				$pages = 1;
				for ($i = 1; $i <= $pages && $i <= M3_MAX_SERIES_PAGES; $i++)
				{
					$raw = '';
					try
					{
						$raw = scrape_url(M3_SERIES_INFO.urlencode($item['seriesId']).'&page='.$i);

						if ($i === 1)
						{
							$page_ids = array_map('intval', extract_ids($raw, 'open?series='.urlencode($item['seriesId']).'&page=', '"'));
							if (count($page_ids) > 0)
							{
								$pages = max($page_ids);
							}
						}

						$series_ids = extract_ids($raw, '<div class="show-bg" style="background-image: url(https://nemzetiarchivum.hu/images/m3/', ')"></div>');
						$program_ids = array_merge($program_ids, $series_ids);
					}
					catch (Throwable $error)
					{
						$errors++;
						$this->_save_failed_response('series-'.preg_replace('/[^A-Za-z0-9]/', '', $item['seriesId']).'-'.$i, $raw);
						log_message('error', 'WEEKLY CRON: series page failed: '.$error->getMessage());
						break; // later pages of a broken series are unlikely to work
					}
				}
			}
			elseif (isset($item['id']))
			{
				$program_ids[] = $item['id'];
			}
		}

		$new_items = array();
		$missing_ids = $this->m3->return_missing_program_ids($program_ids);
		foreach ($missing_ids as $id)
		{
			$raw = '';
			try
			{
				$res = scrape_json(M3_ITEM_INFO.$id, $raw); // $id passed is_program_id()
				if (is_array($res))
				{
					$new_items[] = $res;
				}
				else
				{
					log_message('error', 'WEEKLY CRON: upstream has no item '.$id);
				}
			}
			catch (Throwable $error)
			{
				$errors++;
				$this->_save_failed_response('item-'.$id, $raw);
				log_message('error', 'WEEKLY CRON: item '.$id.' failed: '.$error->getMessage());
			}
		}

		return $new_items;
	}

	public function backup()
	{
		set_time_limit(300);

		if ( ! $this->_begin('backup', MINIMUM_INTERVAL_WEEKLY))
		{
			return;
		}

		$this->load->dbutil();
		$this->load->helper('file');

		$target = FCPATH.'public/m3-db.gz';
		$backup = $this->dbutil->backup(array('add_drop' => false));

		// write to a temp file first so a half-written backup is never served
		$tmp = $target.'.tmp';
		if ($backup && file_put_contents($tmp, $backup, LOCK_EX) !== false && rename($tmp, $target))
		{
			$this->_stamp('backup', time());
			$res = json_encode(get_file_info($target));
			log_message('debug', 'BACKUP: '.$res);
			$this->_end($res);
		}
		else
		{
			@unlink($tmp);
			log_message('error', 'BACKUP FAILED');
			$this->_end('ERROR: backup failed', FALSE);
		}
	}

	public function csv()
	{
		set_time_limit(300);

		if ( ! $this->_begin('csv', MINIMUM_INTERVAL_DAILY))
		{
			return;
		}

		$this->load->dbutil();
		$this->load->helper('file');
		$this->load->model('m3');

		$target = FCPATH.'public/m3-db.csv.gz';
		$csv = $this->dbutil->csv_from_result($this->m3->return_programs_csv_query(), ';');

		$tmp = $target.'.tmp';
		if (file_put_contents($tmp, gzencode($csv), LOCK_EX) !== false && rename($tmp, $target))
		{
			$this->_stamp('csv', time());
			$res = json_encode(get_file_info($target));
			log_message('debug', 'CSV: '.$res);
			$this->_end($res);
		}
		else
		{
			@unlink($tmp);
			log_message('error', 'CSV FAILED');
			$this->_end('ERROR: csv export failed', FALSE);
		}
	}

	/**
	 * One-off maintenance: re-fetches stored programs from id $from_id down to 1 and updates them in place.
	 * CLI only:  php index.php cron refresh <from_id>
	 * Disables itself when finished (delete application/cache/cron-refresh.disabled to run again).
	 */
	public function refresh($from_id = 0)
	{
		if ( ! is_cli())
		{
			show_404();
		}

		$disabled = $this->_cache_file('refresh.disabled');
		if (file_exists($disabled))
		{
			$this->_end('Disabled. Delete '.$disabled.' to run again.', FALSE);
			return;
		}

		$id = (int) $from_id;
		if ($id <= 0)
		{
			$this->_end('Usage: php index.php cron refresh <from_id>', FALSE);
			return;
		}

		$this->load->model('m3');
		$this->load->helper('curl');

		$updated = 0;
		$failed = 0;
		for (; $id > 0; $id--)
		{
			$program = $this->m3->get_program_id_by_id($id);
			if ( ! $program || ! isset($program['program_id']))
			{
				continue;
			}

			try
			{
				$info = scrape_json(M3_ITEM_INFO.$program['program_id']);
				if ( ! is_array($info))
				{
					throw new UnexpectedValueException('upstream has no such item');
				}
				$updated += $this->m3->update_program($id, $this->m3->parse_program($info));
			}
			catch (Throwable $error)
			{
				$failed++;
				log_message('error', 'REFRESH '.$program['program_id'].' failed: '.$error->getMessage());
			}

			usleep(300000); // be gentle with the upstream
		}

		@touch($disabled);
		$this->_end('updated: '.$updated.', failed: '.$failed);
	}

	/**
	 * Fetches one upstream JSON source, parses it and stores the new rows.
	 *
	 * @param string   $label  name used in logs and for saved failed responses
	 * @param string   $url
	 * @param callable $parse  receives the decoded JSON, returns program rows (throws on bad shape)
	 * @return array(int new rows, string|null error message)
	 */
	private function _ingest($label, $url, $parse)
	{
		$raw = '';
		try
		{
			$data = scrape_json($url, $raw);
			return array($this->m3->insert_ignore_programs(call_user_func($parse, $data)), NULL);
		}
		catch (Throwable $error)
		{
			$this->_save_failed_response($label, $raw);
			log_message('error', 'INGEST '.$label.' failed: '.$error->getMessage());
			return array(0, $label.': '.$error->getMessage());
		}
	}

	/**
	 * Starts a job: throttle check + exclusive lock.
	 * Returns FALSE (after producing the output) when the job must not run now.
	 */
	private function _begin($name, $min_interval)
	{
		$diff = time() - $this->_stamp($name);
		if ($diff < $min_interval)
		{
			$this->_end('diff:'.$diff);
			return FALSE;
		}

		$this->lock = fopen($this->_cache_file($name.'.lock'), 'c');
		if ($this->lock === FALSE || ! flock($this->lock, LOCK_EX | LOCK_NB))
		{
			$this->_end('already running');
			return FALSE;
		}

		return TRUE;
	}

	/** Produces the response. A failed job gets HTTP 502 / CLI exit code 1 so schedulers notice. */
	private function _end($output, $ok = TRUE)
	{
		if ( ! $ok)
		{
			set_status_header(502);
		}

		$this->load->view('cron', array('output' => $output));

		if ( ! $ok && is_cli())
		{
			$this->output->_display();
			exit(1);
		}
	}

	private function _cache_file($name)
	{
		return APPPATH.'cache/cron-'.$name;
	}

	/** Reads (no argument) or writes the last successful run timestamp of a job. */
	private function _stamp($name, $new_value = NULL)
	{
		$file = $this->_cache_file($name.'.stamp');

		if ($new_value !== NULL)
		{
			return file_put_contents($file, (string) $new_value, LOCK_EX);
		}

		return is_readable($file) ? (int) trim(file_get_contents($file)) : 0;
	}

	/** Keeps the raw upstream response of a failed run in backup/ for debugging. */
	private function _save_failed_response($label, $raw)
	{
		if ( ! is_string($raw) || $raw === '')
		{
			return;
		}

		$label = preg_replace('/[^A-Za-z0-9_-]/', '', $label);
		@file_put_contents(FCPATH.'backup/'.$label.'-'.date('YmdHis').'.txt', $raw);
	}
}
