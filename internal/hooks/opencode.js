// spindle for OpenCode 2. Save as ~/.config/opencode/plugins/spindle.js:
//   spin install opencode > ~/.config/opencode/plugins/spindle.js
//
// Memory tools: merge into ~/.config/opencode/opencode.json
//   "mcp": {"spindle": {"type": "local", "command": [__SPIN__, "mcp", "--agent", "opencode"]}}
//
// OpenCode has no command hooks, so this plugin stands in for them: it sends
// each event to `spin hook --agent opencode` in the shape Claude Code's hooks
// use, and puts the context that comes back where the model reads it.
//
//   session "context"           SessionStart: the index goes in the system prompt of every request
//   session "compaction"        PreCompact; the next request gets a fresh index
//   session "experimental.idle" Stop
//
// OpenCode keeps sessions in its database and hands plugins only the window
// since the last compaction, so the plugin writes each session's user and
// assistant messages to <SPINDLE_HOME>/opencode/<session>.jsonl for
// ingest. It only appends, one line per message, so a line never moves.
// Subagent sessions are left alone.
//
// Undo: delete this file and the "spindle" entry under "mcp".
import { spawn } from "node:child_process"
import { appendFile, mkdir, readFile } from "node:fs/promises"
import { homedir } from "node:os"
import { join } from "node:path"

const SPIN = __SPIN__
const HOME = process.env.SPINDLE_HOME || join(homedir(), ".spindle")

export default {
  id: "spindle",
  setup: async (ctx) => {
    if (process.env.SPINDLE_INNER) return // a side-model call spindle started: never recurse
    const directory = ctx.location.directory
    const sessions = new Map()

    // state is what the plugin keeps for one session.
    const state = (id) => {
      let s = sessions.get(id)
      if (!s) {
        s = { start: undefined, compacted: false, info: undefined, written: undefined, queue: Promise.resolve("") }
        sessions.set(id, s)
      }
      return s
    }
    const info = (id) => {
      const s = state(id)
      s.info ??= ctx.session.get({ sessionID: id }).catch(() => undefined)
      return s.info
    }
    const subagent = async (id) => Boolean((await info(id))?.parentID)

    // hook runs one event and resolves to the context to add, or "". Like the
    // command hooks, it never fails the session.
    const hook = (event, session, extra = {}) =>
      new Promise((resolve) => {
        const p = spawn(SPIN, ["hook", "--agent", "opencode"], { stdio: ["pipe", "pipe", "ignore"] })
        let out = ""
        p.stdout.on("data", (d) => (out += d))
        p.on("error", () => resolve(""))
        p.on("close", () => {
          try {
            resolve(JSON.parse(out).hookSpecificOutput.additionalContext || "")
          } catch {
            resolve("")
          }
        })
        p.stdin.on("error", () => {})
        p.stdin.end(JSON.stringify({ hook_event_name: event, session_id: session, cwd: directory, ...extra }))
      })

    // transcript appends the session's new messages and resolves to the
    // file's path, or "". Calls for one session run one at a time.
    const transcript = (id) => {
      const s = state(id)
      s.queue = s.queue.then(() => append(id, s)).catch(() => "")
      return s.queue
    }
    const append = async (id, s) => {
      const dir = join(HOME, "opencode")
      const path = join(dir, id + ".jsonl")
      if (!s.written) {
        s.written = new Set()
        const old = await readFile(path, "utf8").catch(() => "")
        for (const line of old.split("\n")) {
          try {
            const mid = JSON.parse(line).mid
            if (mid) s.written.add(mid)
          } catch {}
        }
        if (!old) {
          await mkdir(dir, { recursive: true })
          const i = await info(id)
          await appendFile(path, JSON.stringify({ type: "session", agent: "opencode", id, cwd: directory, parentID: i?.parentID }) + "\n")
        }
      }
      let lines = ""
      for (const m of await ctx.session.context({ sessionID: id })) {
        if ((m.type !== "user" && m.type !== "assistant") || s.written.has(m.id)) continue
        const text =
          m.type === "user"
            ? m.text
            : m.content
                .filter((c) => c.type === "text")
                .map((c) => c.text.trim())
                .filter(Boolean)
                .join("\n\n")
        lines += JSON.stringify({ type: "message", mid: m.id, role: m.type, time: iso(m.time?.created), text }) + "\n"
        s.written.add(m.id)
      }
      if (lines) await appendFile(path, lines)
      return path
    }

    await ctx.session.hook("context", async (event) => {
      try {
        const id = event.sessionID
        if (await subagent(id)) return
        const s = state(id)
        s.start ??= hook("SessionStart", id, { source: s.compacted ? "compact" : "startup" })
        const start = await s.start
        if (start) event.system.push({ type: "text", text: start })
      } catch {}
    })

    await ctx.session.hook("compaction", async (event) => {
      try {
        const id = event.sessionID
        if (await subagent(id)) return
        await hook("PreCompact", id, { transcript_path: await transcript(id) })
        const s = state(id)
        s.start = undefined
        s.compacted = true
      } catch {}
    })

    // Awaited: `opencode run` exits as soon as the session is idle, which
    // would kill a Stop still starting. spin only queues the job here.
    await ctx.session.hook("experimental.idle", async (event) => {
      try {
        const id = event.sessionID
        if (await subagent(id)) return
        await hook("Stop", id, { transcript_path: await transcript(id) })
      } catch {}
    })
  },
}

// iso reads a message time, which may arrive as millis, a DateTime or a string.
function iso(t) {
  const ms = typeof t === "number" ? t : (t?.epochMilliseconds ?? Date.parse(t))
  return new Date(Number.isFinite(ms) ? ms : Date.now()).toISOString()
}
