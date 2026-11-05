<?php defined('BASEPATH') OR exit('*');

class M3 extends CI_Model
{
	/** Value stored in `programs.released` when the upstream gives no usable date. */
	const RELEASED_UNKNOWN = '1970-01-01 00:00:00';

	/** Column limits (characters) of the `programs` table, see .sql/m3.skeleton.sql. */
	private $max_lengths = array(
		'program_id' => 31,
		'title' => 255,
		'subtitle' => 255,
		'country' => 3,
		'quality' => 15,
		'pg' => 15,
		'duration' => 15,
		'ratio' => 15,
		'seriesId' => 255
	);

	public function __construct()
	{
		parent::__construct();
		$this->load->database();
		$this->load->helper('parser');
	}

	public function parse_programs($data)
	{
		$programs = array();

		if (is_array($data))
		{
			foreach ($data as $program)
			{
				try
				{
					$programs[] = $this->parse_program($program);
				}
				catch (Throwable $error)
				{
					log_message('error', 'Unable to parse daily program item: '.$error->getMessage().' '.json_encode($program));
				}
			}
		}

		return $programs;
	}

	public function parse_guides($data)
	{
		$programs = array();

		if (is_array($data))
		{
			foreach ($data as $daily_guides)
			{
				if (is_array($daily_guides))
				{
					$programs = array_merge($programs, $this->parse_programs($daily_guides));
				}
			}
		}

		return $programs;
	}

	/**
	 * Converts one upstream program item into a `programs` table row.
	 *
	 * @param mixed $d decoded upstream item
	 * @return array
	 * @throws InvalidArgumentException when the item is not an array or lacks a valid id / title
	 */
	public function parse_program($d)
	{
		if ( ! is_array($d))
		{
			throw new InvalidArgumentException('Program item is not an array');
		}

		if ( ! isset($d['id']) || ! is_program_id($d['id']))
		{
			throw new InvalidArgumentException('Program item has a missing or malformed id');
		}

		if ( ! isset($d['title']))
		{
			throw new InvalidArgumentException('Program item '.$d['id'].' has no title');
		}

		// The earliest known date is used as the release date.
		// Timestamps are UTC ("...Z"); index.php fixes the PHP timezone, so date() is deterministic.
		$release_ts = 0;
		foreach (array('start_playable_dts', 'start_startTime_dts') as $key)
		{
			if (isset($d[$key]) && is_array($d[$key]))
			{
				foreach ($d[$key] as $dts)
				{
					$t = is_string($dts) ? strtotime($dts) : false;
					if ($t !== false && ($release_ts === 0 || $t < $release_ts))
					{
						$release_ts = $t;
					}
				}
			}
		}

		$seriesId = isset($d['seriesId']) && is_string($d['seriesId']) ? base64_decode($d['seriesId']) : '';

		$row = array(
			'program_id' => $this->_text($d, 'id'),
			'info' => $this->_lines($d, 'info'),
			'extended_info' => $this->_lines($d, 'extended_info'),
			'title' => $this->_text($d, 'title'),
			'subtitle' => $this->_text($d, 'subtitle'), // the upstream sends `false` when there is none
			'description' => $this->_text($d, 'description'),
			'short_description' => $this->_text($d, 'short_description'),
			'company' => $this->_text($d, 'company'),
			'year' => (int) $this->_text($d, 'year'),
			'country' => $this->_text($d, 'country'),
			'creators' => $this->_lines($d, 'creators'),
			'contributors' => $this->_lines($d, 'contributors'),
			'genre' => $this->_lines($d, 'genre'),
			'quality' => $this->_text($d, 'quality'),
			'pg' => $this->_text($d, 'pg'),
			'duration' => $this->_text($d, 'duration'), // HH:MM:SS:FF (FF = frames)
			'ratio' => $this->_text($d, 'ratio'),
			'hasSubtitle' => ! empty($d['hasSubtitle']),
			'isSeries' => ! empty($d['isSeries']),
			'seriesId' => $seriesId ?: '',
			'episode' => (int) $this->_text($d, 'episode'),
			'episodes' => (int) $this->_text($d, 'episodes'),
			'released' => $release_ts ? date('Y-m-d H:i:s', $release_ts) : self::RELEASED_UNKNOWN
		);

		// INSERT IGNORE would silently truncate over-long values; clamp them explicitly and say so.
		foreach ($this->max_lengths as $column => $max)
		{
			if (mb_strlen($row[$column], 'UTF-8') > $max)
			{
				log_message('error', 'Value of '.$column.' truncated for '.$row['program_id']);
				$row[$column] = mb_substr($row[$column], 0, $max, 'UTF-8');
			}
		}
		$row['company'] = mb_strcut($row['company'], 0, 255, 'UTF-8'); // tinytext = 255 bytes

		return $row;
	}

	/** Trimmed scalar value; anything else (null, false, arrays) becomes an empty string. */
	private function _text($d, $key)
	{
		return isset($d[$key]) && is_scalar($d[$key]) && $d[$key] !== false ? trim((string) $d[$key]) : '';
	}

	/** List field joined by newlines; scalars only. */
	private function _lines($d, $key)
	{
		if ( ! isset($d[$key]) || ! is_array($d[$key]))
		{
			return '';
		}

		return trim(implode("\n", array_filter($d[$key], 'is_scalar')));
	}

	public function insert_ignore_programs($programs, $batch_size = 100)
	{
		if ( ! is_array($programs) || count($programs) === 0)
		{
			return 0;
		}

		$keys = array_keys(reset($programs));
		sort($keys);

		$qb_keys = array();
		foreach ($keys as $k)
		{
			$qb_keys[] = $this->db->protect_identifiers($k, FALSE, TRUE);
		}

		$qb_vals = array();
		foreach ($programs as $row)
		{
			ksort($row); // puts $row in the same order as our keys

			$clean = array();
			foreach ($row as $value)
			{
				$clean[] = $this->db->escape($value);
			}

			$qb_vals[] = '('.implode(',', $clean).')';
		}

		$re = 0;
		$records_num = count($qb_vals);
		for ($inserted = 0; $inserted < $records_num; $inserted += $batch_size)
		{
			$next_values = array_slice($qb_vals, $inserted, $batch_size);

			if ($this->db->query($this->_insert_ignore_batch($this->db
				->protect_identifiers('programs', TRUE, TRUE, FALSE), $qb_keys, $next_values)))
			{
				$re += $this->db->affected_rows();
			}
		}

		return $re;
	}

	protected function _insert_ignore_batch($table, $keys, $values)
	{
		return 'INSERT IGNORE INTO '.$table.' ('.implode(', ', $keys).') VALUES '.implode(', ', $values);
	}

	public function get_programs($search = '', $limit = 10, $offset = 0, $select = false)
	{
		if ($select === false)
		{
			$select = array(
				'program_id',
				'title',
				'subtitle',
				'info',
				'extended_info',
				'short_description',
				'description',
				'company',
				'year',
				'country',
				'creators',
				'contributors',
				'genre',
				'quality',
				'pg',
				'duration',
				'ratio',
				'hasSubtitle',
				'isSeries',
				'seriesId',
				'episode',
				'episodes',
				'added',
				'released'
			);
		}

		if ($search)
		{
			// "M3-..." searches by one or more program ids (comma separated), anything else is a text search.
			$program_ids = strtoupper(substr($search, 0, 3)) === 'M3-' ? parse_program_id_list($search, 50) : array();

			if (count($program_ids) > 0)
			{
				$this->db->where_in('program_id', $program_ids);
			}
			else
			{
				$this->db
					->group_start()
					->like('title', $search)
					->or_like('subtitle', $search)
					->or_like('info', $search)
					->or_like('extended_info', $search)
					->or_like('description', $search)
					// ->or_like('short_description', $search)
					// ->or_like('company', $search)
					->or_like('creators', $search)
					->or_like('contributors', $search)
					->or_like('genre', $search)
					->or_where('program_id', $search)
					->group_end();
			}
		}

		$total = $this->db
			->count_all_results('programs', FALSE);

		$items = $this->db
			->select($select)
			->limit($limit, $offset)
			->order_by('id', 'DESC')
			->get()
			->result_array();

		return array(
			'total' => $total,
			'items' => $items
		);
	}

	/**
	 * Returns those of the given program ids that are not stored yet.
	 * Invalid ids are dropped; the lookup is parameterized (no string concatenation).
	 *
	 * @param string[] $ids
	 * @return string[]
	 */
	public function return_missing_program_ids($ids)
	{
		$ids = array_values(array_unique(array_filter((array) $ids, 'is_program_id')));
		if (count($ids) === 0)
		{
			return array();
		}

		$existing = array();
		foreach (array_chunk($ids, 500) as $chunk)
		{
			$rows = $this->db
				->select('program_id')
				->where_in('program_id', $chunk)
				->get('programs')
				->result_array();

			foreach ($rows as $r)
			{
				$existing[] = $r['program_id'];
			}
		}

		return array_values(array_diff($ids, $existing));
	}

	public function return_programs_csv_query()
	{
		return $this->db
			->select('program_id,title,subtitle,episode,episodes,seriesId,quality,year,duration,short_description,released')
			->order_by('id', 'DESC')
			->get('programs');
	}

	public function get_program_id_by_id($id)
	{
		return $this->db
			->select('program_id')
			->where('id', $id)
			->get('programs')
			->row_array();
	}

	/**
	 * Updates a stored program in place. Unlike REPLACE this keeps the row id and the `added` timestamp.
	 * Returns the number of changed rows.
	 */
	public function update_program($id, $data)
	{
		unset($data['program_id'], $data['id'], $data['added']);

		$this->db->where('id', (int) $id)->update('programs', $data);

		return $this->db->affected_rows();
	}
}
