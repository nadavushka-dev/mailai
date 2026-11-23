// mailai add-on for Pi, written by `mailai addon pi`, which rewrites it on
// upgrade. Load it into one session with `pi -e <this directory>`.
//
// It runs `mailai listen` and hands each line to the agent as a message,
// so mailai never types into this agent's pane (SPEC §7). The line is
// mailai's own one-line wake ("1 unread mailai message from …"), never a
// sender's text; the agent reads its mail with `mailai read`. Mail rules
// live in the daemon: this file only carries lines across.
import { spawn, type ChildProcess } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const here = dirname(fileURLToPath(import.meta.url));

function mailaiBin(): string {
	if (process.env.MAILAI_BIN) return process.env.MAILAI_BIN;
	const local = join(homedir(), ".local", "bin", "mailai");
	return existsSync(local) ? local : "mailai";
}

export default function (pi: ExtensionAPI) {
	let child: ChildProcess | undefined;
	let stopped = true;
	let again: ReturnType<typeof setTimeout> | undefined;

	const start = () => {
		if (stopped || child) return;
		const c = spawn(mailaiBin(), ["listen"], { stdio: ["ignore", "pipe", "ignore"] });
		child = c;
		let buf = "";
		c.stdout?.setEncoding("utf8");
		c.stdout?.on("data", (s: string) => {
			buf += s;
			for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
				const line = buf.slice(0, i).trim();
				buf = buf.slice(i + 1);
				if (!line) continue;
				// followUp: an idle agent starts a turn now; a busy one
				// gets it when its turn ends. Nothing touches the input box.
				pi.sendMessage(
					{ customType: "mailai", content: line, display: true },
					{ triggerTurn: true, deliverAs: "followUp" },
				);
			}
		});
		// `mailai listen` outlives daemon restarts by itself; it ends only
		// when refused (not an agent) or missing. Try again slowly.
		c.on("error", () => {});
		c.on("close", () => {
			if (child === c) child = undefined;
			if (!stopped) again = setTimeout(start, 30_000);
		});
	};

	pi.on("session_start", async () => {
		stopped = false;
		start();
	});
	pi.on("session_shutdown", async () => {
		stopped = true;
		if (again) clearTimeout(again);
		child?.kill();
		child = undefined;
	});
	// The mailai guide, as a skill, for this session only.
	pi.on("resources_discover", () => ({ skillPaths: [join(here, "SKILL.md")] }));
}
