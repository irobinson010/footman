package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/plugins"
)

// The "system" context item (ADR-0043): coarse facts about the computer go
// in the system prompt, after the persona and CoreRules, so the AI answers
// for this machine; the local date and time go in the user turn, so the
// cached prompt prefix stays the same from one request to the next. The
// system_status tool lets the AI look at what the computer is doing now.

// SystemBlock renders facts as the system-prompt block. It is stable: the
// same facts give the same text.
func SystemBlock(f platform.SystemFacts) string {
	var b strings.Builder
	b.WriteString("This computer (the one you run on; answer for it, not for other operating systems):")
	line := func(label, v string) {
		if v = strings.TrimSpace(v); v != "" {
			fmt.Fprintf(&b, "\n- %s: %s", label, v)
		}
	}
	osLine := f.OS
	if f.Distro != "" {
		osLine = f.Distro
		if f.Kernel != "" {
			osLine += " (" + strings.TrimSpace(f.OS+" kernel "+f.Kernel) + ")"
		} else if f.OS != "" && !strings.Contains(strings.ToLower(f.Distro), strings.ToLower(f.OS)) {
			osLine += " (" + f.OS + ")"
		}
	} else if f.Kernel != "" {
		osLine = strings.TrimSpace(osLine + " " + f.Kernel)
	}
	line("Operating system", osLine)
	desk := f.Desktop
	if f.Session != "" {
		if desk == "" {
			desk = f.Session + " session"
		} else {
			desk += " on " + sessionName(f.Session)
		}
	}
	line("Desktop", desk)
	line("Shell", f.Shell)
	cpu := f.CPU
	switch {
	case f.Cores > 0 && f.Threads > 0:
		cpu = strings.TrimSpace(fmt.Sprintf("%s (%d cores, %d threads)", cpu, f.Cores, f.Threads))
	case f.Threads > 0:
		cpu = strings.TrimSpace(fmt.Sprintf("%s (%d logical CPUs)", cpu, f.Threads))
	}
	line("CPU", cpu)
	if f.MemoryBytes > 0 {
		line("Memory", gib(f.MemoryBytes)+" RAM")
	}
	line("Graphics", strings.Join(f.GPUs, "; "))
	line("Locale", f.Locale)
	line("Time zone", f.TimeZone)
	line("Footman runs on", f.Arch)
	return b.String()
}

func sessionName(s string) string {
	switch strings.ToLower(s) {
	case "wayland":
		return "Wayland"
	case "x11":
		return "X11"
	}
	return s
}

// SetSystemContext sets the block of facts about the computer the AI is
// given (SystemBlock), or turns it off with "". With it on, each request
// also carries the local date and time. The daemon sets it from the
// context.local or context.cloud "system" item and changes it live.
func (p *Pipeline) SetSystemContext(block string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.system = block
}

// systemPrompt is the persona's prompt followed by the system block, if on.
// Nothing in it changes between requests, so providers can cache it.
func (p *Pipeline) systemPrompt() (prompt string, withSystem bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.system == "" {
		return p.persona.System, false
	}
	if p.persona.System == "" {
		return p.system, true
	}
	return p.persona.System + "\n\n" + p.system, true
}

// userTurn is the first user message: the request, preceded by the local
// date and time when the system item is on.
func (p *Pipeline) userTurn(text string, withSystem bool) string {
	if !withSystem {
		return text
	}
	now := time.Now
	if p.cfg.Now != nil {
		now = p.cfg.Now
	}
	return "[Local time: " + now().Format("Monday 2 January 2006, 15:04 MST") + "]\n" + text
}

// SystemStatusTool is the name of the built-in read-only tool that reports
// what the computer is doing now.
const SystemStatusTool = "system_status"

// statusTop is how many processes system_status lists in each ranking.
const statusTop = 10

// systemStatusTool exposes platform.SystemInfo's Status to the AI. It is
// read-only (permission Read) and reports process names only, never
// command lines. Its result is untrusted: any program can name its own
// process.
type systemStatusTool struct{ p *Pipeline }

func (systemStatusTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name: SystemStatusTool,
		Description: "Look at what this computer is doing right now: the processes using the most memory " +
			"and CPU (by name and PID), memory and swap use, load average, uptime, free disk space, " +
			"battery and graphics card use. Use it for any question about this machine's current state " +
			"instead of explaining how to check.",
		Schema:     json.RawMessage(`{"type":"object","properties":{}}`),
		Permission: plugins.Read,
	}
}

func (t systemStatusTool) Run(ctx context.Context, _ json.RawMessage) (plugins.Result, error) {
	si, ok := t.p.d.Platform.(platform.SystemInfo)
	if !ok {
		return plugins.Result{Text: "system status is not available on this platform", IsError: true}, nil
	}
	st, err := si.Status(ctx, statusTop)
	if err != nil {
		return plugins.Result{}, err
	}
	return plugins.Result{Text: FormatStatus(st), Untrusted: true}, nil
}

// FormatStatus renders a status compactly, for the AI.
func FormatStatus(st platform.SystemStatus) string {
	var b strings.Builder
	cpu := fmt.Sprintf("CPU: %.0f%% busy", st.CPUBusy)
	if st.Load != [3]float64{} {
		cpu += fmt.Sprintf("; load average %.2f %.2f %.2f", st.Load[0], st.Load[1], st.Load[2])
	}
	if st.Uptime > 0 {
		cpu += "; up " + uptime(st.Uptime)
	}
	b.WriteString(cpu)
	if m := st.Memory; m.TotalBytes > 0 {
		fmt.Fprintf(&b, "\nMemory: %s used of %s (%s available)", gib(m.TotalBytes-min(m.AvailableBytes, m.TotalBytes)), gib(m.TotalBytes), gib(m.AvailableBytes))
		if m.SwapTotalBytes > 0 {
			fmt.Fprintf(&b, "; swap %s used of %s", gib(m.SwapTotalBytes-min(m.SwapFreeBytes, m.SwapTotalBytes)), gib(m.SwapTotalBytes))
		}
	}
	procs := func(title string, ps []platform.Process, val func(platform.Process) string) {
		if len(ps) == 0 {
			return
		}
		b.WriteString("\n" + title + ":")
		for i, p := range ps {
			id := fmt.Sprintf("pid %d", p.PID)
			if p.Count > 0 {
				id = fmt.Sprintf("%d processes", p.Count)
			}
			fmt.Fprintf(&b, "\n%d. %s (%s) %s", i+1, p.Name, id, val(p))
		}
	}
	mem := func(p platform.Process) string { return gib(p.RSSBytes) }
	procs("Top processes by memory", st.TopMemory, mem)
	procs("Top by memory, processes with the same name together", st.TopMemoryByName, mem)
	procs("Top processes by CPU (percent of one CPU)", st.TopCPU, func(p platform.Process) string { return fmt.Sprintf("%.1f%%", p.CPU) })
	if len(st.Disks) > 0 {
		b.WriteString("\nDisks:")
		for _, d := range st.Disks {
			fmt.Fprintf(&b, "\n- %s (%s): %s free of %s", d.Mount, d.FSType, gib(d.FreeBytes), gib(d.TotalBytes))
		}
	}
	for _, bat := range st.Batteries {
		fmt.Fprintf(&b, "\nBattery %s: %d%%", bat.Name, bat.Percent)
		if bat.Status != "" {
			b.WriteString(", " + strings.ToLower(bat.Status))
		}
	}
	for _, g := range st.GPUs {
		fmt.Fprintf(&b, "\nGPU %s: %s of %s memory used, %d%% busy", g.Name, gib(g.MemUsedBytes), gib(g.MemTotalBytes), g.UtilizationPercent)
		if g.TemperatureC > 0 {
			fmt.Fprintf(&b, ", %d°C", g.TemperatureC)
		}
	}
	return b.String()
}

// gib formats bytes in GiB, or MiB under one GiB.
func gib(n uint64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

func uptime(d time.Duration) string {
	d = d.Truncate(time.Minute)
	days := int(d / (24 * time.Hour))
	h := int(d/time.Hour) % 24
	m := int(d/time.Minute) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
