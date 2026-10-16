# Build context: the extracted Linux deployment bundle.
FROM debian:12-slim

ENV TZ=Asia/Shanghai \
    PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/playwright-browsers

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

# 先装驱动与 Chromium：这一步只依赖 playwright 二进制（版本跟着 go.mod 锁，很少变），
# 所以主程序更新时这层不会失效 —— 否则每换一次 hd2_bot 都要重新下几百 MB 的 Chromium。
COPY playwright /app/playwright
RUN chmod 755 /app/playwright \
    && /app/playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*

# 主程序放最后：它是唯一经常变的文件，放在这里就不会把上面的浏览器层冲掉。
COPY hd2_bot /app/hd2_bot
RUN chmod 755 /app/hd2_bot

CMD ["/app/hd2_bot"]
