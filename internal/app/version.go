package app

// Version is the agent build version. The build script overrides it via
// -ldflags "-X kidsroom-agent/internal/app.Version=...".
var Version = "dev"
