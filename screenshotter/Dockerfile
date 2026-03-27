FROM mcr.microsoft.com/playwright:v1.52.0-jammy

WORKDIR /app

RUN npm init -y >/dev/null 2>&1 && npm install playwright@1.52.0 >/dev/null 2>&1

COPY screenshotter/server.js /app/server.js

USER pwuser

ENV PORT=9010

CMD ["node", "/app/server.js"]
