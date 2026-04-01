//go:build !darwin

package main

func setMacAppIcon(_ []byte) error { return nil }
func hideMacDockIcon()             {}
