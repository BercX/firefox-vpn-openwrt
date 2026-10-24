package logx

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestLogLevelFiltersOutputAndBuffer(t *testing.T) {
	previousLevel, previousVerbose, previousOutput := minimumLevel.Level(), Verbose.Load(), logOutput
	t.Cleanup(func() {
		minimumLevel.Set(previousLevel)
		Verbose.Store(previousVerbose)
		SetOutput(previousOutput)
	})
	for _, level := range []string{"debug", "info", "warn", "error", "off"} {
		var output bytes.Buffer
		SetOutput(&output)
		SetLevel(level)
		before := Logs.Since(0)
		var after uint64
		if len(before) != 0 {
			after = before[len(before)-1].ID
		}
		Debug("debug")
		Info("info")
		Warn("warn")
		Error("error")
		got := []string{}
		for _, entry := range Logs.Since(after) {
			got = append(got, entry.Message)
			if !strings.Contains(output.String(), entry.Message) {
				t.Fatalf("%s: buffer entry absent from system output: %s", level, entry.Message)
			}
		}
		all := []string{"debug", "info", "warn", "error"}
		start := 0
		for start < len(all) && all[start] != level {
			start++
		}
		if !reflect.DeepEqual(got, all[start:]) || strings.Count(output.String(), "\n") != len(got) {
			t.Fatalf("%s: got %v and output %q, want %v", level, got, output.String(), all[start:])
		}
		if (Target("private.example") == "private.example") != (level == "debug") {
			t.Fatalf("%s: destination detail must require Debug", level)
		}
	}
}
