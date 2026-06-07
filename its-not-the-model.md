# It's not the model

*Your project is fragmented. Your agents are falling through the gaps into hallucinations.*

Someone needs to call a function they've never called before. Maybe it's you. Maybe it's that agent you spun up ten seconds ago. Adding in the call shouldn't be too hard. The tutorial explains what the function does at a high level. The API reference lists every function. A Stack Overflow answer from 2019 *almost* works. Forty-five minutes later, you've got three browser tabs and a grep window open. Meanwhile your agent's a dozen tool calls in and about to confidently invent a function that doesn't exist. The docs aren't missing. They're everywhere. They just don't add up. Some have aged out of sync with the code while others were never built to fit.

When the agent inevitably invents that function, the easy story is that the model isn't smart enough. They say: "We simply need bigger models, fewer hallucinations. Wait for the next frontier model release." But the human developer next to your agent just spent forty-five minutes on the same problem, with no model in the loop. The model isn't what's failing. What's failing is what the model (and the developer) were trying to assemble from the pieces in front of them.

You're relying on a fragmented project. The pieces exist; documentation, configuration, logs, tests, examples. They're often individually fine but they don't form a whole. 

There are two common ways a project fragments. The first is **time drift**. The artifact and the thing it refers to *were* aligned. They've separated since. The tutorial worked in v1.2; the API moved in v2; the tutorial didn't move with it. The test was sharp the day it was written but the function under test got refactored twice, and the test now asserts on something that no longer matters. The variable name `sync` once held a boolean; it now stores an enumeration, and the whole module reads nonsensically. Same shape as concept drift in ML and config drift in dev ops. We see it all the time.

The second is **domain drift**. The artifact and the thing it refers to are both correct, both current. They just live in different domains. The user guide says "invite people to a meeting." The API reference says `events.insert` with `start.dateTime` in RFC3339, an `attendees[]` array, a `conferenceData.createRequest` to spawn the video link, and a `sendUpdates: "all"` flag the docs warn you about. Different vocabularies, different levels of abstraction, written by different people on different days for different readers. Neither is wrong. Someone (you, your agent, the next person to read it) pays a translation tax every time they cross domains. This is the kind nobody has a word for, and it's the one doing more damage in the agentic era.


Both kinds are measurable, and they are easy to test for:

*To see time drift:* pick any reference artifact in your project — a doc page, a test, a code comment, a variable name, an architecture diagram. Find its last meaningful update. Find the last meaningful change to the code it refers to. If the code moved more recently than the artifact, you have time drift. Bonus: actually run what the artifact describes — the tutorial's first code block, the example in the docstring, the integration test's setup. Count the edits required to make it work today. That number is your time drift, measured in commits.


*To see domain drift:* pick a sentence from a guide, an issue title, or a Slack message from your PM. Now find the exact code path that does what the sentence describes. Time yourself, and count the vocabulary jumps: "rate limit" to `TokenBucket.acquire`, "users can share" to `share_permissions.grant`, "sandboxed I/O" to `Directory.Open`. If it took more than thirty seconds and a mental translation, that's domain drift. The number of jumps measures your distance between domains.

Run both, on something you actually work with. The result is almost always worse than you expect. You've been paying these taxes the whole time without measuring them. Your agent has too — just compressed into tokens and tool calls.

Here's one I measured. In a large open-source project, the documentation directory held about 1,600 markdown files. Out of all of them, fifteen mentioned a specific function from the project's API by name. Fifteen. The reference docs were complete; every function listed, every signature documented. The concept docs were rich with hundreds of overview pages explaining how the system worked. They almost never linked. The gap between "here's what the system does" and "here's the call you'd write" was, in practice, the reader's problem.

I checked another codebase. Same pattern. The number isn't the point — the *shape* is. Once you go looking for domain drift, you find it everywhere. As humans, we've grown used to this tax. **Frankly, it forms the basis of most expertise.** The problem in the agentic era is that agents will gladly hallucinate when the going gets tough - leaving humans to go back and clean up the pieces.

Both kinds of fragmentation are organizational, not technical.

Time drift happens because code ships faster than the things that describe it. The engineer fixing the bug is on a deadline; the doc update is a follow-up that gets bumped behind the next deadline; the follow-up never comes. The artifacts age slower than the code, and the gap compounds - even more so when agents are involved. 

Domain drift happens for a different reason. The high-level guide and the function reference get written by different humans on different days, on different teams, sometimes decades apart, for different intended readers. The concept-doc writer is teaching mental models. The reference-doc writer is documenting a contract. **Neither is doing the join.** The join isn't anyone's job. Everyone owns their layer. Nobody owns the bridge.

Neither is the kind of problem a smarter model fixes. You can throw a better model at a fragmented project and it'll still have to traverse the same gaps. The net effect is to make the guesses faster and the errors more confident. Fragmentation is a property of the project, not the consumer.

The cost is paid in different currencies but it's the same bill.

A developer pays in minutes. Thirty to sixty per unfamiliar subsystem, every time. Across a team and a year, that's weeks of engineering time lost to gaps between concept and call. You won't see it on any dashboard, because the time is distributed across every Jira ticket, hidden inside "implementation."

An agent pays in tool calls, tokens, and hallucinations. Every time you spin one up against a fragmented project, it does what any reasonable reader would do: it bridges the gaps with a guess. Sometimes the bridge holds. Sometimes it falls through, and invents a function that doesn't exist, with a confident signature, and produces code that looks correct until it doesn't compile. Or deletes your production database. The model didn't fail. The project didn't give it enough to win.

Hallucination is the symptom you see. Fragmentation is what's actually doing the work.

The fix isn't a smarter model. The fix is a project that knows where its pieces are.

Mechanically: this means keeping a record of which artifacts refer to which code. Check that record at the moments fragmentation is most likely to be introduced; every commit, every release, every doc edit. Surface the gaps as findings, not failures. For time drift, the question is *did the artifact get touched after the last meaningful change to its referent?*, answerable with a script you can write in an afternoon. For domain drift, the question is harder but still tractable: *for each thing the concept layer claims, does anything in the reference layer back it up?* — answerable with a small map between the two.

None of this is novel. What's novel is treating it as work worth doing, and treating the result as ground truth your agent gets to read before it tries to answer. I've been building a small tool that does this for one codebase; the details are over here [link]. The point of this post isn't the tool. The point is that detecting fragmentation is something a project can do, almost no project does, and the absence is why your agent looks dumber than your model.

Two questions to carry away.

When something in your project confuses you, or your frontier agent, don't ask *is the model good enough?* Ask:

*When was this last in sync with the code?* That's time drift.

*How many vocabulary jumps does it take to get from this artifact to the thing it describes?* That's domain drift.

Run those two questions on the next confused agent output or frustrated dev-Slack message you hit. The answer will tell you what's actually broken, and it almost never lives in the model.
