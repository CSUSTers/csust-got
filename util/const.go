package util

import "time"

var (
	// TimeZoneCST China Standard time zone.
	TimeZoneCST, _ = time.LoadLocation("Asia/Shanghai")
	// TimeFormat time format.
	TimeFormat = "2006/01/02-15:04:05"
)
