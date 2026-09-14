// Keep this syntax executable by old Node versions so the error is actionable.
if (process.versions.node.split(".")[0] !== "22") {
  process.stderr.write(
    "SignalWatch requires Node.js 22. Switch Node versions (for example: nvm use) before running npm or make web-dev.\n",
  );
  process.exit(1);
}
