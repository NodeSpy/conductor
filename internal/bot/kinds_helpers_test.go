package bot

// kd is a valid knowledge document whose body is body.
func kd(body string) string {
	return "---\ntitle: a fact\nevidence: seen in tests\nconfidence: high\n---\n" + body
}

// rd is a valid runbook document whose body is body.
func rd(body string) string {
	return "---\ntitle: a procedure\nwhen_to_use: when tested\nsteps:\n  - look\n  - act\nverification: it worked\nrollback: undo it\nowner: sre\n---\n" + body
}
