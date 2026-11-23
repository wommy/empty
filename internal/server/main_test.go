package server_test

import (
	"testing"

	"github.com/wommy/empty/internal/leakcheck"
)

func TestMain(m *testing.M) { leakcheck.Main(m) }
