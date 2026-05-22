# crm-svc container — built with the `tesseract` build tag so the
# KTP OCR endpoint can resolve KTP_OCR_PROVIDER=tesseract at runtime
# (see ktp_provider_tesseract.go). Without this tag the binary still
# runs, but the only available provider is the stub.
#
# Runtime stage is Debian-slim because tesseract-ocr-ind (Indonesian
# language data) ships as a Debian package. Alpine has tesseract but
# the ind pack requires a manual blob download — not worth the saving.
#
# Build from the repo root so the build context contains go.mod, the
# whole module tree, and the migrations dir:
#
#   docker build -f backend/cmd/crm-svc/Dockerfile -t ion/crm-svc .
#
# Runtime env:
#
#   DATABASE_URL=postgres://...
#   KTP_OCR_PROVIDER=tesseract     # opt in; default is stub
#   PORT=8083                       # listens here

FROM golang:1.21-bookworm AS build
WORKDIR /src

# Cache module downloads independently of source changes.
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# CGO is not required — the tesseract provider shells out to the CLI
# rather than linking gosseract. Keep CGO_ENABLED=0 so the binary is
# statically linked and survives the slim runtime image.
ENV CGO_ENABLED=0
RUN go build -tags=tesseract -ldflags='-s -w' \
    -o /out/crm-svc ./cmd/crm-svc

FROM debian:bookworm-slim AS runtime
# tesseract-ocr ships the engine; tesseract-ocr-ind is the Indonesian
# trained-data the KTP layout matches. ca-certificates is needed for
# any outbound TLS the service makes (uploads, billing-svc, etc.).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        tesseract-ocr \
        tesseract-ocr-ind \
        ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Sanity: fail the build if Indonesian data isn't present.
RUN tesseract --list-langs 2>&1 | grep -q '^ind$'

# Non-root user — the container has no business running as root.
RUN groupadd --system ion && useradd --system --gid ion --shell /sbin/nologin ion
USER ion

COPY --from=build /out/crm-svc /usr/local/bin/crm-svc

ENV PORT=8083
EXPOSE 8083

ENTRYPOINT ["/usr/local/bin/crm-svc"]
