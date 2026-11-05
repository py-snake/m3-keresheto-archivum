<?php
defined('BASEPATH') OR exit('No direct script access allowed');

if ( ! function_exists('scrape_url')) {
	/**
	 * Fetches a URL and returns the response body.
	 *
	 * TLS certificates are always verified. Throws a RuntimeException on
	 * transport errors and on any non-2xx response, so callers can never
	 * mistake an error page (or `false`) for real data.
	 *
	 * @param string $url
	 * @param string $cookies optional Cookie header value
	 * @param string $agent   optional User-Agent override
	 * @return string response body (may be an empty string)
	 * @throws RuntimeException
	 */
	function scrape_url($url, $cookies = '', $agent = '')
	{
		if ($agent === '') {
			// Always a regular browser User-Agent. Keep the version current (a years old Chrome stands out);
			// override with the UPSTREAM_USER_AGENT environment variable without touching the code.
			$agent = m3_env('UPSTREAM_USER_AGENT', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36');
		}

		$ch = curl_init();
		curl_setopt($ch, CURLOPT_URL, $url);
		curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
		curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, true);
		curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, 2);
		curl_setopt($ch, CURLOPT_FOLLOWLOCATION, true);
		curl_setopt($ch, CURLOPT_MAXREDIRS, 3);
		curl_setopt($ch, CURLOPT_CONNECTTIMEOUT, 10);
		curl_setopt($ch, CURLOPT_TIMEOUT, 60);
		curl_setopt($ch, CURLOPT_ENCODING, '');
		curl_setopt($ch, CURLOPT_USERAGENT, $agent);
		if ($cookies !== '') {
			curl_setopt($ch, CURLOPT_COOKIE, $cookies);
		}

		$raw = curl_exec($ch);
		$errno = curl_errno($ch);
		$error = curl_error($ch);
		$status = (int) curl_getinfo($ch, CURLINFO_RESPONSE_CODE);

		if (PHP_VERSION_ID < 80000) {
			curl_close($ch); // no-op since PHP 8.0, deprecated in 8.5
		}

		if ($raw === false || $errno !== 0) {
			throw new RuntimeException('HTTP request failed ('.$errno.'): '.$error.' ['.$url.']');
		}

		if ($status < 200 || $status >= 300) {
			throw new RuntimeException('Unexpected HTTP status '.$status.' ['.$url.']');
		}

		return $raw;
	}
}

if ( ! function_exists('scrape_json')) {
	/**
	 * Fetches a URL and decodes the JSON response.
	 *
	 * The upstream API answers 200 + `null` for unknown items, so a `null`
	 * return value is a valid "not found" result and callers must check it.
	 * The upstream also serves JSON with a text/html content type; that is fine.
	 *
	 * @param string $url
	 * @param string $raw receives the raw response body (for saving failed responses)
	 * @return mixed decoded JSON (array|scalar|null)
	 * @throws RuntimeException on transport errors, UnexpectedValueException on invalid JSON
	 */
	function scrape_json($url, &$raw = null)
	{
		$raw = scrape_url($url);
		$data = json_decode($raw, true);

		if (json_last_error() !== JSON_ERROR_NONE) {
			throw new UnexpectedValueException('Invalid JSON ('.json_last_error_msg().') ['.$url.']');
		}

		return $data;
	}
}
