package ollamamem

import (
	"context"
	"testing"
)

// vmStatSample keeps vm_stat's layout with made-up counts.
const vmStatSample = `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                                   100000.
Pages active:                                1500000.
Pages inactive:                               900000.
Pages speculative:                             50000.
Pages throttled:                                   0.
Pages wired down:                             600000.
Pages purgeable:                               20000.
"Translation faults":                     123456789.
Pages copy-on-write:                        1234567.
Pages zero filled:                         12345678.
Pages reactivated:                            12345.
Pages purged:                                 12345.
File-backed pages:                            300000.
Anonymous pages:                             2000000.
Pages stored in compressor:                   800000.
Pages occupied by compressor:                 400000.
Decompressions:                               12345.
Compressions:                                 12345.
Pageins:                                      12345.
Pageouts:                                       123.
Swapins:                                          0.
Swapouts:                                         0.
`

// Inactive pages count as used, as they do for Ollama; purgeable and
// file-backed pages count as free.
func TestParseVMStatCountsFreeAsOllamaDoes(t *testing.T) {
	const total = 16 << 30
	got, ok := parseVMStat(vmStatSample, total)
	used := uint64(1500000+900000+50000+600000+400000-20000-300000) * 4096
	if !ok || got != total-used {
		t.Fatalf("free = %d, %v; want %d", got, ok, total-used)
	}
}

func TestParseVMStatRejectsIncompleteOutput(t *testing.T) {
	for name, input := range map[string]string{
		"empty":        "",
		"no page size": "Pages active: 1.\nPages inactive: 1.\n",
		"missing keys": "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 10.\n",
	} {
		if _, ok := parseVMStat(input, 1<<30); ok {
			t.Errorf("%s: parsed", name)
		}
	}
	if _, ok := parseVMStat(vmStatSample, 0); ok {
		t.Error("parsed without a total")
	}
}

func TestParseVMStatNeverUnderflows(t *testing.T) {
	got, ok := parseVMStat(vmStatSample, 4<<30)
	if !ok || got != 0 {
		t.Fatalf("free = %d, %v; want 0 when use exceeds the total", got, ok)
	}
}

func TestParseMemAvailable(t *testing.T) {
	meminfo := "MemTotal:       32594424 kB\nMemFree:         1200000 kB\nMemAvailable:   20480000 kB\nBuffers:          123 kB\n"
	got, ok := parseMemAvailable(meminfo)
	if !ok || got != 20480000*1024 {
		t.Fatalf("MemAvailable = %d, %v", got, ok)
	}
	if _, ok := parseMemAvailable("MemTotal: 1 kB\nMemFree: 1 kB\n"); ok {
		t.Fatal("parsed meminfo without MemAvailable")
	}
}

func TestFreeMemoryOverride(t *testing.T) {
	t.Setenv(FreeMemoryEnv, "12345")
	if got, ok := FreeMemory(context.Background()); !ok || got != 12345 {
		t.Fatalf("FreeMemory = %d, %v", got, ok)
	}
}
