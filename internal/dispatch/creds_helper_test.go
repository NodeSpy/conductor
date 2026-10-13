package dispatch

// ghCreds are the credentials a github event's work receives, as the github
// connector declares them: the write token under GH_TOKEN (and its aliases),
// the read token under PC_GH_APP_TOKEN, both as template keys.
func ghCreds(user, app string) Credentials {
	return Credentials{
		Env:       map[string]string{"GH_TOKEN": user, "GITHUB_TOKEN": user, "PC_GH_WRITE_TOKEN": user, "PC_GH_APP_TOKEN": app},
		Templates: map[string]string{"gh_token": user, "app_token": app},
	}
}
