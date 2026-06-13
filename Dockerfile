ARG TARGETARCH

FROM alpine:latest
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

ARG TARGETARCH
# Copy the pre-built binary based on the target architecture
COPY dist/linux_${TARGETARCH}/seedstream .

EXPOSE 7000
EXPOSE 119
CMD ["./seedstream"]
