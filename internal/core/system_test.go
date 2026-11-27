package core_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/permissions"
	"github.com/irobinson010/footman/internal/platform"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
)

var testFacts = platform.SystemFacts{
	OS: "Linux", Distro: "Ubuntu 24.04 LTS", Kernel: "6.8.0-1-generic",
	Desktop: "KDE", Session: "wayland", Shell: "bash",
	CPU: "AMD Ryzen 9 5900X 12-Core Processor", Cores: 12, Threads: 24,
	MemoryBytes: 64 << 30, GPUs: []string{"NVIDIA GeForce RTX 4060 Ti", "AMD Raphael"},
	Locale: "en_US.UTF-8", TimeZone: "America/Chicago", Arch: "linux/amd64",
}

func TestSystemBlock(t *testing.T) {
	got := core.SystemBlock(testFacts)
	want := `This computer (the one you run on; answer for it, not for other operating systems):
- Operating system: Ubuntu 24.04 LTS (Linux kernel 6.8.0-1-generic)
- Desktop: KDE on Wayland
- Shell: bash
- CPU: AMD Ryzen 9 5900X 12-Core Processor (12 cores, 24 threads)
- Memory: 64.0 GiB RAM
- Graphics: NVIDIA GeForce RTX 4060 Ti; AMD Raphael
- Locale: en_US.UTF-8
- Time zone: America/Chicago
- Footman runs on: linux/amd64`
	if got != want {
		t.Errorf("SystemBlock =\n%s\nwant\n%s", got, want)
	}
	if core.SystemBlock(testFacts) != got {
		t.Error("SystemBlock is not stable")
	}
	// Unknown fields are left out.
	if got := core.SystemBlock(platform.SystemFacts{OS: "Windows", Threads: 8}); strings.Contains(got, "Desktop") || !strings.Contains(got, "Windows") || !strings.Contains(got, "8 logical CPUs") {
		t.Errorf("sparse SystemBlock = %q", got)
	}
}

// With the system item on, the facts follow the persona's prompt, the time
// is in the user turn, and the prompt is the same from one request to the
// next (so the provider's cache keeps working). Off, neither is sent.
func TestSystemContextInPrompt(t *testing.T) {
	now := time.Date(2026, 11, 26, 14, 5, 0, 0, time.FixedZone("EST", -5*3600))
	r := newRig(t, func(_ *core.Deps, c *core.Config) {
		c.System = "PERSONA"
		c.Now = func() time.Time { return now }
	})
	block := core.SystemBlock(testFacts)
	r.p.SetSystemContext(block)

	r.ai.Queue(lfake.Reply{Text: "Firefox."}, lfake.Reply{Text: "Still Firefox."})
	r.submit(core.TriggerWake, "Footman, what is using the most memory?")
	now = now.Add(3 * time.Minute)
	r.submit(core.TriggerWake, "Footman, and now?")

	reqs := r.ai.Requests()
	if len(reqs) != 2 {
		t.Fatalf("AI called %d times, want 2", len(reqs))
	}
	if want := "PERSONA\n\n" + block + "\n\n" + core.WebRules(false); reqs[0].System != want {
		t.Errorf("system prompt = %q, want %q", reqs[0].System, want)
	}
	if reqs[1].System != reqs[0].System {
		t.Errorf("system prompt changed between turns:\n%q\n%q", reqs[0].System, reqs[1].System)
	}
	if strings.Contains(reqs[0].System, "14:05") {
		t.Error("the time is in the cached system prompt")
	}
	if got, want := reqs[0].Messages[0].Text, "[Local time: Thursday 26 November 2026, 14:05 EST]\nwhat is using the most memory?"; got != want {
		t.Errorf("first user turn = %q, want %q", got, want)
	}
	if got := reqs[1].Messages[0].Text; !strings.HasPrefix(got, "[Local time: Thursday 26 November 2026, 14:08 EST]\n") {
		t.Errorf("second user turn = %q", got)
	}

	// Turned off (the user tightened context.cloud): no facts, no time.
	r.p.SetSystemContext("")
	r.ai.Queue(lfake.Reply{Text: "Hello."})
	r.submit(core.TriggerWake, "Footman, hello")
	reqs = r.ai.Requests()
	last := reqs[len(reqs)-1]
	if last.System != "PERSONA\n\n"+core.WebRules(false) {
		t.Errorf("system prompt with system off = %q", last.System)
	}
	if got := last.Messages[0].Text; got != "hello" {
		t.Errorf("user turn with system off = %q", got)
	}
}

// system_status is offered, passes the default permission policy (read is
// automatic) and returns the platform's status compactly, as untrusted
// data.
func TestSystemStatusTool(t *testing.T) {
	r := newRig(t, func(d *core.Deps, _ *core.Config) {
		d.Gate = &permissions.Gate{} // the default policy from config
	})
	plat := r.plat
	plat.SysStatus = platform.SystemStatus{
		CPUBusy: 12, Load: [3]float64{1.5, 1, 0.5}, Uptime: 26 * time.Hour,
		Memory: platform.Memory{TotalBytes: 64 << 30, AvailableBytes: 40 << 30, SwapTotalBytes: 8 << 30, SwapFreeBytes: 8 << 30},
		TopMemory: []platform.Process{
			{Name: "firefox", PID: 4242, RSSBytes: 3 << 30},
			{Name: "code", PID: 99, RSSBytes: 512 << 20},
		},
		TopMemoryByName: []platform.Process{{Name: "Isolated Web Co", Count: 12, RSSBytes: 5 << 30}},
		TopCPU:          []platform.Process{{Name: "kwin_wayland", PID: 1200, CPU: 7.5}},
		Disks:           []platform.Disk{{Mount: "/", FSType: "ext4", TotalBytes: 1 << 40, FreeBytes: 300 << 30}},
		GPUs:            []platform.GPUStatus{{Name: "NVIDIA GeForce RTX 4060 Ti", MemUsedBytes: 2 << 30, MemTotalBytes: 16 << 30, UtilizationPercent: 5, TemperatureC: 41}},
	}
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "s1", Name: core.SystemStatusTool, Args: json.RawMessage(`{}`)}}},
		lfake.Reply{Text: "Firefox, at 3 gigabytes."},
	)
	tools := bus.Subscribe(r.bus, core.ToolCalled, 8)
	r.submit(core.TriggerWake, "Footman, what process is using the most memory?")

	reqs := r.ai.Requests()
	if len(reqs) != 2 {
		t.Fatalf("AI called %d times, want 2", len(reqs))
	}
	var offered bool
	for _, d := range reqs[0].Tools {
		offered = offered || d.Name == core.SystemStatusTool
	}
	if !offered {
		t.Fatal("system_status was not offered")
	}
	res := reqs[1].Messages[len(reqs[1].Messages)-1]
	if res.Role != llm.ToolResult || res.IsError {
		t.Fatalf("tool result = %+v", res)
	}
	for _, want := range []string{
		"CPU: 12% busy; load average 1.50 1.00 0.50; up 1d 2h",
		"Memory: 24.0 GiB used of 64.0 GiB (40.0 GiB available); swap 0 MiB used of 8.0 GiB",
		"1. firefox (pid 4242) 3.0 GiB",
		"2. code (pid 99) 512 MiB",
		"1. Isolated Web Co (12 processes) 5.0 GiB",
		"1. kwin_wayland (pid 1200) 7.5%",
		"- / (ext4): 300.0 GiB free of 1024.0 GiB",
		"GPU NVIDIA GeForce RTX 4060 Ti: 2.0 GiB of 16.0 GiB memory used, 5% busy, 41°C",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("status lacks %q:\n%s", want, res.Text)
		}
	}
	if plat.StatusTop != 10 {
		t.Errorf("asked for the top %d, want 10", plat.StatusTop)
	}
	ev := await(t, tools, func(core.ToolRan) bool { return true })
	if ev.Name != core.SystemStatusTool || ev.IsError || ev.Confirmation != "auto" {
		t.Errorf("ToolRan = %+v, want system_status allowed automatically", ev)
	}
}

// A platform without SystemInfo offers no system_status.
func TestSystemStatusNotOfferedWithoutSystemInfo(t *testing.T) {
	r := newRig(t, func(d *core.Deps, _ *core.Config) {
		d.Platform = noSystemInfo{d.Platform.(*pfake.Platform)}
	})
	r.ai.Queue(lfake.Reply{Text: "Hi."})
	r.submit(core.TriggerWake, "Footman, hi")
	for _, d := range r.ai.Requests()[0].Tools {
		if d.Name == core.SystemStatusTool {
			t.Fatal("system_status offered on a platform without SystemInfo")
		}
	}
}

// noSystemInfo hides the fake platform's SystemInfo.
type noSystemInfo struct{ *pfake.Platform }

func (noSystemInfo) Facts() {}
