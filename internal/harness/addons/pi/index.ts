// mailai add-on for Pi, written by `mailai addon pi`, which rewrites it on
// upgrade. Install it once for every session (~/.pi/agent/extensions/mailai)
// or for one (`pi -e <this directory>`); it is active only for an agent
// switched on in mailai's config ([addon] agents) and stays out otherwise.
//
// It runs `mailai listen` and hands each line to the agent as a message,
// so mailai never types into this agent's pane (SPEC §7). The line is
// mailai's own one-line wake ("1 unread mailai message from …"), never a
// sender's text; the agent reads its mail with `mailai read`. Mail rules
// live in the daemon: this file only carries lines across.
import { execFile, spawn, type ChildProcess } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const here = dirname(fileURLToPath(import.meta.url));
const NOT_SWITCHED_ON = 3; // `mailai listen` exit code ([addon] agents)

function mailaiBin(): string {
	if (process.env.MAILAI_BIN) return process.env.MAILAI_BIN;
	const local = join(homedir(), ".local", "bin", "mailai");
	return existsSync(local) ? local : "mailai";
}

// check: "on", "off" (not switched on, or no mailai here), or "unknown"
// (the daemon is down: listen retries by itself).
function check(): Promise<"on" | "off" | "unknown"> {
	return new Promise((resolve) => {
		execFile(mailaiBin(), ["listen", "--check"], { timeout: 5000 }, (err) => {
			if (!err) return resolve("on");
			const e = err as NodeJS.ErrnoException & { code?: unknown };
			if (e.code === "ENOENT" || e.code === NOT_SWITCHED_ON) return resolve("off");
			resolve("unknown");
		});
	});
}

export default function (pi: ExtensionAPI) {
	let child: ChildProcess | undefined;
	let stopped = true;
	let state: "on" | "off" | "unknown" = "off";
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
		// `mailai listen` outlives daemon restarts by itself. It ends when
		// this agent is not switched on (stop) or mailai is missing (stop);
		// anything else, try again slowly.
		let missing = false;
		c.on("error", (e: NodeJS.ErrnoException) => {
			if (e.code === "ENOENT") missing = true;
		});
		c.on("close", (code) => {
			if (child === c) child = undefined;
			if (missing || code === NOT_SWITCHED_ON) {
				stopped = true;
				return;
			}
			if (!stopped) again = setTimeout(start, 30_000);
		});
	};

	pi.on("session_start", async () => {
		state = await check();
		if (state === "off") return;
		stopped = false;
		start();
	});
	pi.on("session_shutdown", async () => {
		stopped = true;
		if (again) clearTimeout(again);
		child?.kill();
		child = undefined;
	});
	// The mailai guide, as a skill, only where the add-on is on.
	pi.on("resources_discover", () => (state === "on" ? { skillPaths: [join(here, "SKILL.md")] } : {}));
}
