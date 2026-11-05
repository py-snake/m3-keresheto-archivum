<?php
defined('BASEPATH') OR exit('No direct script access allowed');

class App extends CI_Controller {

	public function index()
	{
		//$this->output->enable_profiler(TRUE);//DEBUG

		$this->load->helper(array('form', 'url'));
		$this->load->model('m3');
		$this->load->library('pagination');

		$limit = 10;
		$offset = intval($this->uri->segment(1)) ? (intval($this->uri->segment(1)) - 1) * $limit : 0;
		// the search runs LIKE queries on several text columns, so the term length is capped
		$search = mb_substr(trim((string)$this->input->get('kereses', TRUE)), 0, 100, 'UTF-8');
		$select = $this->input->get('nyers') === '' ? '*' : false;

		$programs = $this->m3->get_programs($search, $limit, $offset, $select);

		$this->pagination->initialize(array(
			'base_url' => site_url(''),
			'uri_segment' => 1,
			'num_links' => 4,
			'per_page' => $limit,
			'total_rows' => $programs['total']
		));

		$links = $this->pagination->create_links();

		$this->load->view('head', array(
			'search' => $search
		));
		$this->load->view('search', array(
			'search' => $search
		));
		$this->load->view('list'.($select?'-raw':''), array(
			'items' => $programs['items'], 
			'links' => $links,
			'total' => $programs['total']
		));
		$this->load->view('foot');
	}

	/**
	 * POST /add  (id=M3-...,M3-...)
	 * Stores programs that are on the M3 site but not in the database yet.
	 * Public on purpose (the search form uses it), therefore: POST only, same-origin only,
	 * strict id validation, a small id cap and a per-client rate limit.
	 */
	public function add()
	{
		if ($this->input->method(TRUE) !== 'POST')
		{
			$this->output->set_header('Allow: POST');
			return $this->_json(array('error' => 'method not allowed'), 405);
		}

		if ( ! $this->_is_same_origin())
		{
			return $this->_json(array('error' => 'forbidden'), 403);
		}

		$this->load->helper('parser');
		$program_ids = parse_program_id_list($this->input->post('id'), M3_ADD_MAX_IDS);
		if (count($program_ids) === 0)
		{
			return $this->_json(array('error' => 'no valid program id'), 400);
		}

		if ( ! $this->_rate_limit_ok())
		{
			$this->output->set_header('Retry-After: '.M3_ADD_RATE_WINDOW);
			return $this->_json(array('error' => 'too many requests'), 429);
		}

		$this->load->model('m3');
		$this->load->helper('curl');

		$new_items = array();
		$failed = 0;
		foreach ($this->m3->return_missing_program_ids($program_ids) as $id)
		{
			try
			{
				$item = scrape_json(M3_ITEM_INFO.$id); // upstream answers `null` for unknown ids
				if (is_array($item))
				{
					$new_items[] = $item;
				}
			}
			catch (Throwable $error)
			{
				$failed++;
				log_message('error', 'ADD '.$id.' failed: '.$error->getMessage());
			}
		}

		$added = $this->m3->insert_ignore_programs($this->m3->parse_programs($new_items));

		return $this->_json(array('added' => $added, 'failed' => $failed), $failed > 0 && $added === 0 ? 502 : 200);
	}

	/** Browsers always send the Origin header on POST; it must match the Host of this request. */
	private function _is_same_origin()
	{
		$origin = (string) $this->input->get_request_header('Origin');
		$host = (string) $this->input->server('HTTP_HOST');
		$parts = parse_url($origin);

		if ($origin === '' || $host === '' || ! is_array($parts) || empty($parts['host']))
		{
			return FALSE;
		}

		$origin_host = $parts['host'].(isset($parts['port']) ? ':'.$parts['port'] : '');

		return strcasecmp($origin_host, $host) === 0;
	}

	/** Fixed window counter per client IP (stored with the CI file cache). */
	private function _rate_limit_ok()
	{
		$this->load->driver('cache', array('adapter' => 'file'));

		$key = 'add_rl_'.md5($this->input->ip_address());
		$count = (int) $this->cache->get($key);
		if ($count >= M3_ADD_RATE_LIMIT)
		{
			return FALSE;
		}

		$this->cache->save($key, $count + 1, M3_ADD_RATE_WINDOW);

		return TRUE;
	}

	private function _json($data, $status = 200)
	{
		$this->output
			->set_status_header($status)
			->set_content_type('application/json')
			->set_output(json_encode($data));
	}
}
