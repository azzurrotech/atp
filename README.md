AZZURROTECH PLATFORM (ATP)

A complete local platform featuring AI inference, file storage, client websites, email services, and version control, all orchestrated via Docker Compose with Caddy reverse proxy.

SERVICES OVERVIEW

1. OLLAMA (Core)
   - Profile: core
   - Endpoint: http://localhost:11434
   - Purpose: Local LLM inference engine (runs Gemma, Llama, etc.)
   - Always starts unless explicitly disabled.

2. OPEN WEBUI
   - Profile: webui
   - Endpoint: https://ai.localhost
   - Purpose: Chat interface for interacting with Ollama models.
   - Requires: Ollama, Caddy.

3. NEXTCLOUD
   - Profile: storage
   - Endpoint: https://nc.localhost
   - Purpose: Secure file storage with optional AI integration.
   - Requires: Database (MariaDB), Ollama, Caddy.

4. WORDPRESS
   - Profile: website
   - Endpoint: https://site.localhost
   - Purpose: Client-facing website CMS.
   - Requires: Database (MariaDB), Caddy.

5. GITEA
   - Profile: vcs
   - Endpoint: https://git.localhost
   - Purpose: Self-hosted Git version control system.
   - SSH Port: 2222
   - Requires: Database (MariaDB), Caddy.

6. STALWART MAIL
   - Profile: mail
   - Endpoint: https://mail.localhost (Admin UI)
   - Ports: 25 (SMTP), 587 (Submission), 993 (IMAPS), 4190 (Sieve)
   - Purpose: Full-featured email server (SMTP/IMAP/JMAP).
   - Requires: Caddy (for Admin UI), TLS certificates.

7. CADDY
   - Profile: proxy
   - Endpoint: Handles all HTTPS traffic for *.localhost domains.
   - Purpose: Reverse proxy, automatic HTTPS (self-signed for local), SSL termination.
   - Requires: All web services.

8. AUTHENTIK

    Profile: auth
    Endpoint: https://authentik.localhost
    Purpose: Centralized Identity Provider (SSO) for Nextcloud, WordPress, Gitea, and Open WebUI.
    Requires: Redis, PostgreSQL (dedicated), Caddy.


QUICK START GUIDE

STEP 1: PREREQUISITES
- Ensure Docker and Docker Compose (v2.0+) are installed.
- (Optional) NVIDIA GPU drivers and Container Toolkit for GPU acceleration.
- Edit your system hosts file to map local domains to 127.0.0.1:
  - Linux/Mac: /etc/hosts
  - Windows: C:\Windows\System32\drivers\etc\hosts

  Add the following lines:
  127.0.0.1 ai.localhost
  127.0.0.1 nc.localhost
  127.0.0.1 site.localhost
  127.0.0.1 git.localhost
  127.0.0.1 mail.localhost

STEP 2: CONFIGURE ENVIRONMENT
- Copy the example environment file:
  cp .env.example .env
- Edit the .env file to set your desired passwords, admin emails, and domain names.
- IMPORTANT: Do not commit the .env file to version control.

STEP 3: START SERVICES
You can start the platform in different configurations using the start.sh script or Docker Compose profiles directly.

Option A: Start Core Only (Ollama)
  ./start.sh
  (Or: docker compose up -d)

Option B: Start All Services
  ENABLE_ALL=1 ./start.sh
  (Or: docker compose --profile all up -d)

Option C: Start Specific Services
  To start only the storage suite (Nextcloud + DB):
  ENABLE_STORAGE=1 ./start.sh
  (Or: docker compose --profile storage up -d)

  To start only the website (WordPress + DB):
  ENABLE_WEBSITE=1 ./start.sh
  (Or: docker compose --profile website up -d)

  To start only the mail server:
  ENABLE_MAIL=1 ./start.sh
  (Or: docker compose --profile mail up -d)

STEP 4: VERIFY AND ACCESS
- Wait for the script to complete (it checks health of services and pulls models).
- Access the services via your browser:
  - Open WebUI: https://ai.localhost
  - Nextcloud: https://nc.localhost (User: admin, Pass: see .env)
  - WordPress: https://site.localhost (User: admin, Pass: see .env)
  - Gitea: https://git.localhost (User: admin, Pass: see .env)
  - Stalwart Admin: https://mail.localhost (User: admin, Pass: see .env)

POST-RUN SETUP INSTRUCTIONS

OPEN WEBUI SETUP
1. Navigate to https://ai.localhost.
2. Create an administrator account.
3. In the model selector (top left), choose a model (e.g., llama3.2 or gemma3:26b).
4. If no models appear, the start script should have pulled them. If not, run:
   docker compose exec ollama ollama pull llama3.2

NEXTCLOUD AI INTEGRATION
1. Login to Nextcloud at https://nc.localhost.
2. Go to Administration Settings -> Apps.
3. Search for "AI Assistant" or "Text" and enable the app.
4. Go to Administration Settings -> AI.
5. Set the Provider to "Ollama".
6. Set the Endpoint to: http://ollama:11434
7. Set the Model Name to: llama3.2
8. Save and test by typing a prompt in a document.

WORDPRESS SETUP
1. Navigate to https://site.localhost.
2. The installer should auto-run. If not, follow the on-screen wizard.
3. Use the admin credentials defined in your .env file.
4. (Optional) Install an AI plugin like "AI Engine" and configure it to point to http://ollama:11434 for content generation.

GITEA SETUP
1. Navigate to https://git.localhost.
2. Complete the initial setup wizard using the admin credentials from .env.
3. To push/pull code via SSH:
   ssh-keygen -t ed25519 -C "your_email@example.com"
   Add your public key to your Gitea profile.
   Clone repo: git clone ssh://git@git.localhost:2222/username/repo.git

STALWART MAIL SETUP
1. Navigate to https://mail.localhost.
2. Login with the admin credentials from .env.
3. Create a domain (e.g., mail.localhost).
4. Create mailboxes (e.g., postmaster@mail.localhost).
5. For real-world email delivery (sending/receiving from external providers), you must configure DNS records on your domain registrar:
   - MX Record: Points to your server's public IP (or mail.localhost if using a tunnel).
   - SPF Record: v=spf1 mx -all
   - DKIM: Generate the DKIM key in the Stalwart Admin panel and add it as a TXT record.
   - Note: For local testing only, you can send emails between internal accounts without DNS.

AUTHENTIK SETUP

    Navigate to https://authentik.localhost.
    Complete the initial setup wizard (create admin user).
    Configure Applications:
        Go to Applications -> Create.
        Add applications for Nextcloud, WordPress, Gitea, and Open WebUI using their respective https://...localhost URLs.
        Configure the OIDC or SAML settings in each target application (Nextcloud, Gitea, etc.) to point to the Authentik endpoints provided in the application settings.
    Configure Flows: Set up authentication flows (e.g., username/password) and bind them to the applications.
    Users: Create users in Authentik and assign them to groups. These users can now log in to all integrated services using their single Authentik credentials.


FILE STRUCTURE REFERENCE

azzurrotech-platform/
|-- .env                  (Configuration and secrets - DO NOT COMMIT)
|-- docker-compose.yml    (Service definitions and profiles)
|-- Caddyfile             (Reverse proxy routing rules)
|-- start.sh              (Automated startup and health check script)
|-- README.md             (This documentation)
|-- wordpress/
|   |-- uploads.ini       (PHP configuration for file uploads)
|-- stalwart/
|   |-- config.toml       (Mail server configuration)
|   |-- certs/            (Place TLS certificates here if using custom ones)

TROUBLESHOOTING COMMON ISSUES

ISSUE: Ollama container fails to start.
FIX: If you do not have an NVIDIA GPU, the container may fail due to missing drivers.
1. Edit docker-compose.yml.
2. Locate the 'ollama' service.
3. Delete the entire 'deploy' block (starts with 'deploy:' and ends before 'healthcheck').
4. Run: docker compose down -v && docker compose up -d

ISSUE: "Address already in use" error.
FIX: Another process is using a port (e.g., 11434, 80, 443).
1. Check which process is using the port:
   Linux/Mac: lsof -i :11434
   Windows: netstat -ano | findstr :11434
2. Kill the process or change the port mapping in docker-compose.yml.

ISSUE: ATP services cannot connect to each other.
FIX: Ensure all containers are running and healthy.
1. Run: docker compose ps
2. Check logs for errors: docker compose logs -f ollama
3. Ensure the .env file has correct database passwords matching across services.

ISSUE: HTTPS Certificate Errors in Browser.
FIX: Since this is a local environment, Caddy generates self-signed certificates.
1. Your browser will warn you about the connection.
2. Click "Advanced" -> "Proceed to site (unsafe)" or "Accept the risk and continue".
3. This is expected behavior for local domains like ai.localhost.

UPDATES AND MAINTENANCE

UPDATE IMAGES
docker compose pull

REBUILD AND RESTART
docker compose up -d --force-recreate

BACKUP ATP DATA
To backup all persistent data (models, files, databases):
1. Stop the platform: docker compose down
2. Tar the volumes:
   docker run --rm -v azzurrotech-platform_ollama_data:/data -v $(pwd)/backup:/backup alpine tar czf /backup/ollama_backup.tar.gz /data
   docker run --rm -v azzurrotech-platform_nextcloud_data:/data -v $(pwd)/backup:/backup alpine tar czf /backup/nextcloud_backup.tar.gz /data
   (Repeat for other volumes as needed)

RESTORE ATP DATA
1. Stop the platform.
2. Extract the tar files into the corresponding volume paths (requires stopping Docker first or using specific restore commands).
3. Start the platform: docker compose up -d

SECURITY NOTES
- All passwords are stored in the .env file. Ensure this file has restricted permissions (chmod 600 .env).
- Never expose ports 25, 587, or 993 to the public internet without proper firewall rules and DNS configuration.
- Self-signed certificates are used for local testing. For production, replace the Caddyfile to use real domains and Let's Encrypt.
- The AzzurroTech Platform is designed for local development. Harden all services before exposing to the internet.

SUPPORT RESOURCES
- Ollama Documentation: https://ollama.ai
- Open WebUI GitHub: https://github.com/open-webui/open-webui
- Nextcloud Admin Manual: https://docs.nextcloud.com
- Gitea Documentation: https://docs.gitea.com
- Stalwart Labs Docs: https://stalwartlabs.com/documentation

### Supporting Open Source

The AzzurroTech Platform relies on the following open-source projects. We recommend a monthly donation of $10 CAD to each to support their continued development:

| Project | Description | Donation Link |
| :--- | :--- | :--- |
| **Ollama** | Local LLM runner | [Donate via GitHub Sponsors](https://github.com/sponsors/ollama) |
| **Open WebUI** | Chat interface for LLMs | [Donate via GitHub Sponsors](https://github.com/sponsors/open-webui) |
| **Nextcloud** | File sync & share | [Donate to Nextcloud GmbH](https://nextcloud.com/contribute/) |
| **WordPress** | Web publishing platform | [Donate to WordPress Foundation](https://wordpressfoundation.org/donate/) |
| **Gitea** | Painless self-hosted Git | [Donate via GitHub Sponsors](https://github.com/sponsors/go-gitea) |
| **Stalwart Labs** | Modern mail server | [Support Stalwart](https://stalwartlabs.com/contact) |
| **Caddy** | Secure web server | [Donate via GitHub Sponsors](https://github.com/sponsors/mholt) |
| **MariaDB** | Open source database | [Donate to MariaDB Foundation](https://mariadb.org/donate/) |

*Note: Donation links may redirect to GitHub Sponsors, Open Collective, or the project's official donation page. Contributions are voluntary.*