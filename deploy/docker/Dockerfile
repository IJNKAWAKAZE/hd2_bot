# Build context: the extracted Linux deployment bundle.
FROM debian:12-slim

ENV TZ=Asia/Shanghai \
    PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/playwright-browsers

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY hd2_bot playwright /app/
RUN chmod 755 /app/hd2_bot /app/playwright \
    && /app/playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*

CMD ["/app/hd2_bot"]
