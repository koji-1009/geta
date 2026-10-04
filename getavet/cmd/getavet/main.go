// Command getavet reports the declarations of a geta application that
// geta.New would refuse at startup. See package getavet.
//
//	go run github.com/koji-1009/geta/getavet/cmd/getavet ./...
package main

import (
	"github.com/koji-1009/geta/getavet"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() { singlechecker.Main(getavet.Analyzer) }
