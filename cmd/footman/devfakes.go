package main

// The fakes register themselves as "fake" in their area's registry. They are
// linked into the binary so `footman daemon --dev-fakes` can run where no
// real implementation exists yet; without that flag the daemon never picks
// them.
import (
	_ "github.com/irobinson010/footman/internal/jobs/fake"
	_ "github.com/irobinson010/footman/internal/llm/fake"
	_ "github.com/irobinson010/footman/internal/platform/fake"
	_ "github.com/irobinson010/footman/internal/plugins/fake"
	_ "github.com/irobinson010/footman/internal/speech/fake"
)
