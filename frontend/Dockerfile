# ---- Stage 1: Build ----
FROM node:20-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# ---- Stage 2: Serve ----
FROM nginx:1.25-alpine
COPY --from=build /app/dist /usr/share/nginx/html

# SPA: redirect all routes to index.html
RUN printf 'server {\n\
    listen 80;\n\
    root /usr/share/nginx/html;\n\
    index index.html;\n\
    location ~* \\.html$ {\n\
        add_header Cache-Control "no-store, no-cache, must-revalidate";\n\
        try_files $uri $uri/ /index.html;\n\
    }\n\
    location / {\n\
        try_files $uri $uri/ /index.html;\n\
    }\n\
}\n' > /etc/nginx/conf.d/default.conf

EXPOSE 80
