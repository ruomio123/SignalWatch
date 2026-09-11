package integration_test

import (
	"os"
	"signalwatch/internal/platform/testguard"
	"testing"
)

func TestMain(m *testing.M) { testguard.Install(); os.Exit(m.Run()) }
