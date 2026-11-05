<?php
/**
 * Dependency free tests for the pure helper functions.
 *
 * Run:  php tests/run.php
 * Exit code 0 = everything passed.
 *
 * The database / HTTP behaviour is covered by the manual container checks
 * described in CLAUDE.md (section "Verification recipes").
 */
define('BASEPATH', __DIR__.'/../system/');

// index.php defines m3_env() in the real application; the tests only need the defaults.
if ( ! function_exists('m3_env'))
{
	function m3_env($name, $default = '') { return $default; }
}

require __DIR__.'/../application/helpers/parser_helper.php';
require __DIR__.'/../application/helpers/curl_helper.php';

$failures = 0;
function check($name, $actual, $expected)
{
	global $failures;
	if ($actual === $expected)
	{
		echo "ok   $name\n";
		return;
	}
	$failures++;
	echo "FAIL $name\n     expected: ".var_export($expected, true)."\n     actual:   ".var_export($actual, true)."\n";
}

// is_program_id
check('valid id', is_program_id('M3-59945851A60999998'), true);
check('lowercase id is not canonical', is_program_id('m3-59945851a60999998'), false);
check('sql injection payload', is_program_id("M3-X') OR 1=1 -- "), false);
check('quote', is_program_id("M3-A'B"), false);
check('too long', is_program_id('M3-'.str_repeat('A', 28)), false);
check('max length', is_program_id('M3-'.str_repeat('A', 27)), true);
check('empty suffix', is_program_id('M3-'), false);
check('null', is_program_id(null), false);
check('array', is_program_id(array('M3-A')), false);
check('upstream query string injection', is_program_id('M3-A&x=1'), false);

// parse_program_id_list
check('list: normalises and de-duplicates', parse_program_id_list('m3-aaa1, M3-BBB2 ,M3-AAA1'), array('M3-AAA1', 'M3-BBB2'));
check('list: drops junk', parse_program_id_list("hello,M3-OK1,M3-X') OR 1=1 --,,"), array('M3-OK1'));
check('list: respects limit', parse_program_id_list('M3-A1,M3-A2,M3-A3', 2), array('M3-A1', 'M3-A2'));
check('list: empty', parse_program_id_list(''), array());
check('list: null', parse_program_id_list(null), array());

// extract_ids
check('extract: basic', extract_ids('x[a]y[b]z', '[', ']'), array('a', 'b'));
check('extract: match at offset 0', extract_ids('[a]', '[', ']'), array('a'));
check('extract: missing postfix stops without error', extract_ids('[a][b', '[', ']'), array('a'));
check('extract: nothing', extract_ids('abc', '[', ']'), array());
check('extract: real series page marker',
	extract_ids('<div class="show-bg" style="background-image: url(https://nemzetiarchivum.hu/images/m3/M3-1A)"></div>',
		'<div class="show-bg" style="background-image: url(https://nemzetiarchivum.hu/images/m3/', ')"></div>'),
	array('M3-1A'));

// scrape_url must refuse to talk to anything that is not a successful HTTP response
$threw = false;
try { scrape_url('https://127.0.0.1:1/'); } catch (RuntimeException $e) { $threw = true; }
check('scrape_url throws on connection failure', $threw, true);

echo $failures === 0 ? "\nAll tests passed\n" : "\n$failures test(s) FAILED\n";
exit($failures === 0 ? 0 : 1);
