package apitest

import (
	"regexp"
)

const (
	MemberID = "carol-33333333"
	AdminID  = "bob-22222222"
)

var PathParam = regexp.MustCompile(`\{[^}]+\}`)
