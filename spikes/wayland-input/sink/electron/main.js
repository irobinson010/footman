// Minimal Electron app pointing at websink, standing in for Teams/Slack/VS Code.
// Run: electron --no-sandbox [--ozone-platform=wayland|x11] sink/electron [URL]
const { app, BrowserWindow } = require("electron");
app.whenReady().then(() => {
  const w = new BrowserWindow({ width: 1000, height: 700, title: "electronsink" });
  w.loadURL(process.argv.find((a) => a.startsWith("http")) || "http://127.0.0.1:8799/");
  w.webContents.on("did-finish-load", () => w.webContents.focus());
});
