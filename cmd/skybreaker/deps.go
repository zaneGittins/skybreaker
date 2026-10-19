//go:build !nodeps

package main

import (
	_ "github.com/zaneGittins/skybreaker/internal/configfile/yamlfmt" // YAML config files
	"github.com/zaneGittins/skybreaker/internal/tsauth/tsnetsrv"
)

var tailnet = tsnetsrv.Start
