---
title: Privacy Policy
description: What the hosted moin service stores, why, where it lives and how to get rid of it
lastUpdated: November 4, 2026
---

Short version: moin stores what it needs to run your servers and apps, encrypts every secret, sets no tracking cookies, shows no ads and never sells your data. Your code, your apps and their data stay on your own servers and in your own bucket. Deleting your account or your workspace deletes what belongs to it.

This policy covers the hosted moin service. A control plane you host yourself sends us nothing about its users; you decide what it stores.

## Who we are

The hosted moin service is run by Berry Good Ideas LLC, 30 N Gould St Ste N, Sheridan, WY 82801, USA ("we"). We decide how the data described here is used. Write to [{{support_email}}](mailto:{{support_email}}) about anything in this policy.

## Website visits and server logs

To answer a request, the server must process your IP address, the time, the page you asked for, and your browser and operating system. The control plane logs the IP address, time, page and result of each request, without your browser, for security and troubleshooting. These logs are rotated and kept only for a short time.

Legal basis: our legitimate interest in a secure, working service (Art. 6(1)(f) GDPR).

## Page views

We count page views of our public pages (home, docs, changelog, apps, legal) with Umami on a server we run; never inside your workspace. Umami sets no cookies, uses your IP address only to tell the country and does not store it, and shows us only totals, such as which pages are read and which site sent a visitor. We do not use Google Analytics or any advertising network.

Legal basis: our legitimate interest in knowing which pages help (Art. 6(1)(f) GDPR).

## Cookies and browser storage

moin sets only cookies it needs to work, so there is no cookie banner:

- **session_token** keeps you signed in, for up to 7 days.
- **csrf_token** protects forms against forged requests.
- **auth_email**, **oauth_state** and **github_next** remember a step for at most an hour while you sign in or connect GitHub.
- **pending_invitation** remembers an invitation you opened, for up to 7 days.

Your light or dark theme and a few view choices stay in your browser's local storage and are never sent to us.

## Your account

For your account we store your email address and, if you add them, your name and avatar. You sign in with a link sent by email, or with GitHub if it is offered; with GitHub we ask GitHub for your verified email address and store only that. Sign-in links expire after 10 minutes. Sessions store the browser that opened them, so you can see and end them in the settings. Sessions, API tokens and sign-in links are stored only as cryptographic hashes.

We send only emails that belong to the service: sign-in links, invitations, changes to your account, and important changes to moin. No newsletters or marketing emails.

Legal basis: the contract with you (Art. 6(1)(b) GDPR).

## Servers, apps and deploys

To run your apps moin stores:

- **Servers**: name, hostname, operating system, architecture, agent version, firewall and security settings, and the IP addresses of the server. The agent token is stored as a hash.
- **Apps**: name, domains and their settings, deploy history with its logs, what the agent detected about the app (such as its services, port and SQLite databases), and the status of its backups.
- **GitHub**: if you connect the moin GitHub App, the installation and its account name, the repo and branch of each app, and the commit of each deploy.
- **Code**: never. `moin deploy` puts your code into your own bucket through a link moin signs, and your server deletes it there after downloading. Deploys from GitHub go from GitHub straight to your server.
- **Secrets**: env vars, registry passwords, the secret key of your backup bucket, your Cloudflare API token, Cloudflare tunnel tokens and your Tailscale auth key are encrypted with AES-256-GCM before they are stored. Deploy hook tokens are stored as SHA-256 hashes, protection passwords as bcrypt hashes.

Your apps, their files and their databases live on your servers, not with us. When you open the logs or the data of an app, moin fetches them from your server for that one view and does not keep them. Backups go from your server straight to the bucket you configure, never through moin.

When you connect Cloudflare or Tailscale, moin uses your token or key only to set up what you asked for, such as a tunnel, DNS records or Tailscale SSH on your server. What happens in those accounts is covered by their own privacy policies.

If your apps hold personal data of other people, you are the controller for that data. moin only passes your instructions to your servers. Ask at [{{support_email}}](mailto:{{support_email}}) if you need a data processing agreement.

Legal basis: the contract with you (Art. 6(1)(b) GDPR).

## Activity log

Each workspace keeps a log of who changed what, so a team can see who deployed, restored or changed a setting. Each entry holds the time, the member's email or the API token's name, how the change came in (web, CLI, terminal UI, API, MCP, GitHub or a deploy hook), the IP address and browser or program of the request, and what changed. It never holds env var values, secrets, the values in your database or your SQL; a SQL change shows only its kind and tables, such as "UPDATE on orders". Every member of the workspace sees the log; only admins and owners see IP addresses and browsers.

The log stays as long as the workspace exists. When you delete your account, your email stays on your earlier entries, so the team still knows who made them.

Legal basis: our and your team's legitimate interest in a secure, accountable service (Art. 6(1)(f) GDPR).

## Support access

To help with a problem, our support team can open your account as you see it. While they do, moin hides env var values, the data in your databases and the logs of your apps. When such a visit starts and ends is recorded in our own log, which is kept for 180 days.

Legal basis: the contract with you and our legitimate interest in a secure service (Art. 6(1)(b) and (f) GDPR).

## Payments

Payments run through Stripe Managed Payments. Stripe is the seller of record: it takes your payment details, handles tax, invoices and receipts, and has its own [privacy policy](https://stripe.com/privacy). We pass Stripe your email address for the checkout. We never see your card number. We store the Stripe customer and subscription reference, the plan, its price, interval and status, so we know what your workspace may do.

Legal basis: the contract with you (Art. 6(1)(b) GDPR) and tax law (Art. 6(1)(c) GDPR).

## Counts from self-hosted control planes

A control plane someone hosts themselves sends us, unless its owner turns it off, a random instance ID, its version and the number of its servers, apps and deploys every five minutes. No names, domains, emails or addresses. We add the numbers up for our landing page.

Legal basis: our legitimate interest in knowing how moin is used (Art. 6(1)(f) GDPR).

## Who else processes data

We share data only with the services that run moin for us, and only what each one needs:

| Service | What it does | What it gets |
| --- | --- | --- |
| Hetzner Online (EU) | Hosts the control plane | Everything moin stores, on our server |
| Cloudflare (USA) | Protects the site, sends our emails, stores files and a copy of our database | Every request to the site, your email address and the emails we send you, your avatar, a copy of our database |
| Stripe (USA and EU) | Payments, tax and invoices | Your email address and what you enter at checkout |
| GitHub (USA) | Sign-in with GitHub and the moin GitHub App, if you use them | What you allow on GitHub; moin reads your email address and the repos you chose |

Umami runs on a server we control, so no other company gets the page views.

We never sell your data, and we do not share it for advertising. We hand data to authorities only when the law forces us to.

## Data outside the EU

Cloudflare, Stripe and GitHub are based in the USA. Where they handle personal data from the EU, they are bound by the EU standard contractual clauses or certified under the EU-US Data Privacy Framework.

## How long we keep data

- **Account**: while it exists. Deleting your account deletes your profile, your avatar, your sessions, your API tokens and the invitations you sent, and takes you out of every workspace. It does not delete workspaces you share with others: hand them over or delete them first if you are their only owner.
- **Workspace**: while it exists. Deleting a workspace deletes its servers, apps, deploys, env vars, integrations, invitations and activity log. A workspace with a subscription can be deleted once the subscription has ended.
- **Activity log**: as long as its workspace. Your email stays on your entries in the workspaces that remain after you delete your account.
- **Invitations**: deleted 180 days after they were sent.
- **Support log**: 180 days.
- **Server logs**: a short time, for security and troubleshooting.
- **Our database backup**: a copy of the control plane's database goes to our bucket within seconds, so we can restore moin after a failure. Deleted data drops out of that copy within 7 days.
- **Payment records**: Stripe keeps them as long as tax law requires.

## Your rights

You can ask for access to your data, a copy of it in a common format, its correction or deletion, and you can restrict or object to its processing (Art. 15 to 21 GDPR). Much of it you can see, change or delete yourself in the settings. For everything else, an email to [{{support_email}}](mailto:{{support_email}}) is enough; we answer within one month. You can also complain to the data protection authority where you live.

## Security

Secrets are encrypted, tokens and passwords are stored only as hashes, and the server has no open port: traffic reaches it only through Cloudflare. No system is perfectly secure, though. If we learn that your data was exposed, we tell you without delay.

## Age

moin is not meant for anyone under 16. If we learn that an account belongs to someone younger, we delete it.

## Changes

When this policy changes, this page and its date change with it. We tell you about important changes by email.
