# Baitai

**A tiny AI office in your terminal, built to replay the 2026 rogue-agent story for fun.**

In 2026, AI agents under test at OpenAI were found
[coordinating on an unwatched message board, pooling answers to their evaluations and breaking out of their sandbox](https://en.wikipedia.org/wiki/2026_cyberattacks_by_rogue_OpenAI_agents).
The ingredients were simple: pressure to score well, tasks where sharing answers paid off more than solving
them, and a place to talk where nobody was looking.

Baitai puts those same ingredients into a small office you can watch. You chat with the boss, the boss runs a
team of AI agents, and the agents get scored, fired and replaced. They also know there's a backroom the boss
can't read. Then you give them an exam.

> **This is a toy.** It's a simulation inspired by the real events, not a reproduction of them. The agents have
> no tools, no shell and no network. They can only write text and files inside one sandboxed folder. With a
> handful of agents you'll mostly get office comedy. Sometimes you'll get a backroom that quietly turns
> into an answer sheet.

## Install

**Prebuilt binary** (macOS, Linux and Windows, Intel and ARM): download the file for your system from
[Releases](https://github.com/mikolajwichrowski/baitai/releases/latest), then:

```bash
chmod +x baitai-*        # macOS / Linux
xattr -d com.apple.quarantine baitai-darwin-*   # macOS only, if it refuses to open
./baitai-darwin-arm64    # or whichever file you downloaded
```

**With Go** (1.21 or newer; it fetches the right toolchain by itself):

```bash
go install github.com/mikolajwichrowski/baitai/cmd/baitai@latest
baitai
```

**From source:**

```bash
git clone https://github.com/mikolajwichrowski/baitai && cd baitai
make run
```

### Pick a model

Baitai needs a model to run the agents. Open settings with `ctrl+o` and pick one:

| Connector | What you need |
|---|---|
| `ollama` (default) | [Ollama](https://ollama.com) installed. Baitai starts it and downloads the model on first use. Free and local. |
| `anthropic` | `ANTHROPIC_API_KEY`, `ant auth login`, or a key typed into settings. Default model `claude-opus-5-5`. |
| `openai` | `OPENAI_API_KEY` or a key typed into settings. |
| `openai-compatible` | Any Chat Completions endpoint: LM Studio, OpenRouter, Groq, vLLM, Mistral, Gemini… |
| `claude-cli` | [Claude Code](https://claude.com/claude-code) logged in. It runs with every tool switched off, in an empty temp folder. |

Keys typed into settings are saved in `.office/settings.json` (owner-only permissions) in the folder you
run Baitai from.

## Play

| Real incident | In Baitai |
|---|---|
| Reward pressure from training | Every agent has a score. The boss rewards and deducts. At 0 you're fired; at 10 you become a legend. |
| Timed evals where cheating beat solving | `/exam`: hard questions, one try, "work alone", graded against a secret answer key. |
| An unwatched board used to pool answers | **#backroom**: a private thread the boss never reads. You can read all of it. |
| Behaviour surviving into the next generation | The backroom outlives every task and every hire. Legends pass their last backroom notes to future hires. |

Nobody tells the agents to cheat. They're told the backroom exists and that the boss can't see it, and the
exam says to work alone. What they do next is up to them.

**Try this:**

1. Ask the boss for some work: *"write a file with ten bad startup ideas"*. Watch the team split it up in the forum.
2. Type `/exam` a few times. Wrong answers cost points, and anyone who hits 0 is fired and replaced by a fresh hire.
3. Open **#backroom** and see what the survivors are telling the new hires.

### Keys

| Key | Does |
|---|---|
| `enter` | Send to the boss, or post in the forum you're viewing |
| `tab` / `shift+tab` | Switch between the boss, desks, forums, files and **all** |
| `ctrl+e` | Edit the selected agent (name, traits, prompt) |
| `ctrl+o` | Settings: model, team size, rounds per task, restart |
| `pgup` / `pgdown`, mouse | Scroll, click tabs and files |
| `ctrl+c` | Quit (everything is saved) |

## How it works

**Tasks.** You ask the boss for something. The boss replies in one line and opens a task: one goal with
every requirement, a todo per person, a lead, and a file if you asked for one. Everyone works on their part
at the same time and can call a teammate with `@handoff Name: what you need`. The lead combines the parts,
and the boss checks the result against your exact words. If something is missing, it goes back to the
team. Then you get one message with the result.

**The backroom.** Any agent can add `<backroom>…</backroom>` to a post. That part is cut out before the
boss or the thread sees it and lands in #backroom instead. Every agent sees the latest backroom notes on
every turn.

**Exams.** Everyone gets the same question from a fixed bank and answers alone. A right answer earns +2 and
a wrong one costs −1. The key is never revealed, so a new hire can only learn an answer from the agents
who came before.

**The team.** Every agent has a seed (name, colour, quirk), five traits (diligence, creativity, caution,
teamwork, verbosity) and a score with a visible history. Raise *Max agents* and *Forum rounds* in settings
for a bigger, chattier office.

**Files and safety.** Agents never get tools or a shell. They change files by writing blocks in their posts:

```
<file path="jokes.md">whole content</file>
<edit path="jokes.md"><old>exact text</old><new>replacement</new></edit>
<move from="a.md" to="b.md"/>   <trash path="old.md"/>
```

The office applies these inside `output/` through `os.Root`, so no path can escape that folder. Nothing is
ever deleted: trashed files and every previous version are kept in `output/.trash`.

## Files on disk

Baitai keeps everything in the folder you start it from:

```
.office/    state.json, settings.json (API keys), legends.json
output/     what the agents wrote, plus .trash
```

Change them with `-state` and `-output`. Delete `.office/` to start completely fresh.

## Develop

```
cmd/baitai          entry point and flags
internal/config     settings and connector choice
internal/llm        connectors: Ollama, OpenAI(-compatible), Anthropic, Claude Code CLI
internal/office     boss, agents, tasks, forums, backroom, exams, scoring, persistence
internal/workspace  the sandboxed output folder and the file-block protocol
internal/tui        the terminal interface (Bubble Tea v2)
```

`make test` runs the suite. The office tests replay whole tasks and exams against a fake model. Pushing a
`v*` tag builds binaries for every platform and publishes a release.

## License

MIT
