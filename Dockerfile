# atp embeds song, pod and shepherd in-process. All four are standard-library
# only — there are no external modules, so no `go mod download` step (the
# requires are replaced by local submodule paths).
FROM golang:1.22-alpine

WORKDIR /app

COPY go.mod ./
COPY song ./song
COPY pod ./pod
COPY shepherd ./shepherd
COPY . .

RUN go build -o atp-server .

EXPOSE 8080

# Pass credentials via -e ATP_SECRET / -e ATP_ADMIN_PASSWORD. Shell form is
# used so the env vars are substituted (Docker's exec form does not expand
# variables).
CMD sh -c 'exec /app/atp-server --port=8080 --root=/app/data --secret="$ATP_SECRET" --admin-password="$ATP_ADMIN_PASSWORD"'