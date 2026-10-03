# Contributing

Small project, small rules:

- Keep it **server-rendered, no JavaScript, no external assets**. Target is old e-ink browsers (Tolino, Kindle, Kobo).
- Standard library only unless there is a strong reason.
- `gofmt`, `go vet ./...` and `go test ./...` must pass (CI runs them).
- Never put the API key in HTML, URLs or logs.
- Check Komga endpoints/fields against your instance's `/v3/api-docs` before changing them.

Dev loop:

```sh
cp .env.example .env   # edit
set -a; . ./.env; set +a
go run .
```

Open an issue before large changes. PRs welcome.
