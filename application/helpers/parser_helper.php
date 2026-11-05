<?php
defined('BASEPATH') OR exit('No direct script access allowed');

if ( ! function_exists('extract_ids')) {
	/**
	 * Returns every substring found between $prefix and $postfix.
	 * Stops quietly if a prefix has no closing $postfix.
	 *
	 * @return string[]
	 */
	function extract_ids($string, $prefix = '_PREFIX_', $postfix = '_POSTFIX_') {
		$array = array();
		$offset = 0;

		while (($start = strpos($string, $prefix, $offset)) !== false)
		{
			$start += strlen($prefix);
			$end = strpos($string, $postfix, $start);
			if ($end === false)
			{
				break;
			}

			$array[] = substr($string, $start, $end - $start);
			$offset = $end + strlen($postfix);
		}

		return $array;
	}
}

if ( ! function_exists('is_program_id')) {
	/**
	 * True for a well-formed M3 program id (e.g. M3-59945851A60999998).
	 * Every id that reaches a query or an upstream URL must pass this check.
	 */
	function is_program_id($id) {
		return is_string($id) && preg_match('/^M3-[A-Z0-9]{1,27}$/', $id) === 1;
	}
}

if ( ! function_exists('parse_program_id_list')) {
	/**
	 * Parses a comma separated list of program ids typed by a user.
	 * Case-insensitive; invalid and duplicate entries are dropped; at most $limit ids are returned.
	 *
	 * @return string[]
	 */
	function parse_program_id_list($string, $limit = 20) {
		$ids = array();

		foreach (explode(',', (string) $string) as $part)
		{
			$id = strtoupper(trim($part));
			if (is_program_id($id) && ! in_array($id, $ids, true))
			{
				$ids[] = $id;
				if (count($ids) >= $limit)
				{
					break;
				}
			}
		}

		return $ids;
	}
}
