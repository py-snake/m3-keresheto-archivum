<?php
defined('BASEPATH') OR exit('No direct script access allowed');

$_title = 'm3 kereshető archívum';
if ($search) {
	$_title = html_escape($search) . ' - ' . $_title;
}

?><!DOCTYPE html>
<html>
<head>
	<title><?php echo $_title; ?></title>

	<meta charset="utf-8">
	<meta name="referrer" content="no-referrer">
	<meta name="viewport" content="width=device-width, initial-scale=1">

	<link rel="icon" href="<?php echo base_url('public/logo_100_100.png'); ?>" type="image/x-icon">
	<link rel="shortcut icon" href="<?php echo base_url('public/logo_100_100.png'); ?>" type="image/x-icon">

	<link href="https://fonts.googleapis.com/css?family=Roboto:300,400,500" rel="stylesheet">
	<link href="https://unpkg.com/@mdi/font@7.4.47/css/materialdesignicons.min.css" rel="stylesheet">
	<link href="<?php echo base_url('public/m3.min.css'); ?>" rel="stylesheet">
	<link href="<?php echo base_url('public/app.css'); ?>?v=2" rel="stylesheet">
</head>
<body class="mdc-typography">
	<header>
		<h1 class="mdc-typography--headline5"><?php echo $_title; ?></h1>
	</header>

	<main class="main-content mdc-typography--body2">
