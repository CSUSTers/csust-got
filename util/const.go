package util

import "time"

var (
	TimeZoneCST, _ = time.LoadLocation("Asia/Shanghai")
	TimeFormat     = "2006/01/02-15:04:05"
)
