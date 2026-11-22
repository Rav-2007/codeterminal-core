// WHAT KNOWN PROGRAMS DO, BY TYPE, for autoAccess.ts.
//
// The first table named single lines, and a fresh list of risky lines found
// whole types with no rule (2026-11-22): package managers writing for the whole
// computer, process and service control, migration tools going backwards,
// deploy tools changing what is live. Each type is one function here.
//
// Still best effort, for autoAccess.ts's reason: a program it has never heard
// of, or a script the project ships, is not read.
//
// No 'vscode' import, as there.

export const has = (args: string[], ...any: string[]): boolean => args.some((a) => any.includes(a));
// short finds one letter among bundled short options: -rf has both r and f.
export const short = (args: string[], letter: string): boolean =>
  args.some((a) => /^-[A-Za-z]+$/.test(a) && a.includes(letter));
export const plain = (args: string[]): string[] => args.filter((a) => !a.startsWith('-'));

// toolName is a program or script word as the tool it is: manage.py is manage.
function toolName(word: string): string {
  return (word.split(/[/\\]/).pop() ?? '').toLowerCase()
    .replace(/(.)@[\w.^~-]+$/, '$1')
    .replace(/\.(exe|cmd|bat|py|js|mjs|cjs|ts|phar|sh)$/, '');
}

const PACKAGE_RUNNERS = new Set(['pnpm', 'yarn', 'bun']);

// packageTool is `yarn vercel` as vercel.
export function packageTool(name: string, args: string[]): [string, string[]][] {
  const at = args.findIndex((a) => !a.startsWith('-'));
  return PACKAGE_RUNNERS.has(name) && at >= 0 ? [[toolName(args[at]), args.slice(at + 1)]] : [];
}

// innerTools is every program a line may be running through another: `php
// artisan`, `cargo sqlx`, `docker exec api alembic`.
export function innerTools(name: string, args: string[]): [string, string[]][] {
  const out: [string, string[]][] = [[toolName(name), args], ...packageTool(name, args)];
  const at = args.findIndex((a) => !a.startsWith('-'));
  if (at >= 0 && /^(python[0-9.]*|py|node|php|ruby|cargo|dotnet|ts-node|tsx)$/.test(name)) {
    out.push([toolName(args[at]), args.slice(at + 1)]);
  }
  if (['docker', 'podman', 'docker-compose', 'kubectl'].includes(name)) {
    const run = args.findIndex((a) => a === 'exec' || a === 'run');
    // The program is among the first words: every later one is its own argument.
    const last = Math.min(args.length, run + 24);
    for (let i = run + 1; run >= 0 && i < last; i++) {
      if (!args[i].startsWith('-')) out.push([toolName(args[i]), args.slice(i + 1)]);
    }
  }
  return out;
}

// ---------------------------------------------------------------------------
// Wipes

// wipesData is a tool told to go backwards or start empty.
export function wipesData(name: string, args: string[]): boolean {
  switch (name) {
    case 'dropuser':
      return true;
    case 'alembic':
      return has(args, 'downgrade') && !has(args, '--sql');
    case 'flask':
      return has(args, 'db') && has(args, 'downgrade');
    case 'manage':
      return has(args, 'migrate') && has(args, 'zero');
    case 'prisma':
      return has(args, '--accept-data-loss', '--force-reset');
    case 'drizzle-kit':
      return has(args, 'drop') || (has(args, 'push', 'push:pg', 'push:mysql', 'push:sqlite') && has(args, '--force'));
    case 'knex':
      return has(args, 'migrate:rollback', 'migrate:down');
    case 'sequelize':
    case 'sequelize-cli':
      return args.some((a) => /^db:(drop$|migrate:undo)/.test(a));
    case 'typeorm':
    case 'typeorm-ts-node-esm':
    case 'typeorm-ts-node-commonjs':
      return has(args, 'schema:drop', 'migration:revert');
    case 'artisan':
      return has(args, 'migrate:fresh', 'migrate:reset', 'migrate:refresh', 'migrate:rollback', 'db:wipe');
    case 'rails':
    case 'rake':
      return has(args, 'db:schema:load', 'db:migrate:reset', 'db:rollback');
    case 'mix':
      return has(args, 'ecto.drop', 'ecto.reset', 'ecto.rollback');
    case 'ef':
    case 'dotnet-ef':
      return has(args, 'database') && has(args, 'drop');
    case 'sqlx':
      return (has(args, 'database') && has(args, 'drop', 'reset')) || (has(args, 'migrate') && has(args, 'revert'));
    case 'diesel':
      return (has(args, 'database') && has(args, 'reset')) || (has(args, 'migration') && has(args, 'revert', 'redo'));
    case 'flyway':
      return has(args, 'clean');
    case 'goose':
      return has(args, 'reset', 'down', 'down-to');
    case 'dbmate':
      return has(args, 'drop', 'down', 'rollback');
    case 'migrate':
      return has(args, 'drop', 'down');
    case 'mysqladmin':
      return has(args, 'drop');
    case 'pg_restore':
      return has(args, '--clean') || short(args, 'c');
    case 'rm':
      return plain(args).some((a) => /(^|[/\\])\*$/.test(a));
    case 'cp':
    case 'copy':
      return /^(\/dev\/null|nul)$/i.test(plain(args)[0] ?? '');
    case 'mv':
      return plain(args).slice(-1)[0] === '/dev/null';
    case 'tar':
      return has(args, '--remove-files');
    case 'clear-content':
    case 'clc':
      return true;
  }
  return false;
}

// printsNothing is a command whose only effect behind a ">" is an empty file.
export function printsNothing(name: string, args: string[]): boolean {
  if (name === ':' || name === 'true') return true;
  const words = plain(args);
  return (name === 'cat' || name === 'type') && words.length === 1 && /^(\/dev\/null|nul)$/i.test(words[0]);
}

const CODE_WIPE = /\bdrop\s+(database|schema|table)\b|dropDatabase\s*\(/i;

export function codeWipes(code: string): boolean {
  return CODE_WIPE.test(code) && !code.includes(':memory:');
}

// ---------------------------------------------------------------------------
// Outside the project: this computer, not these files

// A name that every program of its kind shares, the editor and Mochiii included.
const SHARED_NAMES = new Set([
  'node', 'python', 'python3', 'py', 'java', 'ruby', 'php', 'go', 'bash', 'sh', 'zsh', 'code', 'electron', 'chrome',
  'chromium', 'firefox', 'docker', 'dockerd', 'postgres', 'mysqld', 'redis-server', 'ssh', 'dotnet', 'deno', 'bun',
  'mochiii', 'mochiii-daemon', 'systemd', 'explorer', 'powershell', 'pwsh',
]);

export function broadPattern(pattern: string): boolean {
  const p = pattern.toLowerCase().replace(/^\^|\$$/g, '').replace(/\.exe$/, '');
  return SHARED_NAMES.has(p) || /^[.*]*$/.test(p) || p.length < 2;
}

const SYSTEM_PACKAGES: Record<string, RegExp> = {
  brew: /^(install|reinstall|uninstall|remove|rm|upgrade|update|tap|untap|link|unlink|services|cleanup|autoremove)$/,
  apt: /^(install|remove|purge|upgrade|full-upgrade|update|autoremove)$/,
  'apt-get': /^(install|remove|purge|upgrade|dist-upgrade|update|autoremove)$/,
  dnf: /^(install|remove|erase|upgrade|update|autoremove)$/,
  yum: /^(install|remove|erase|upgrade|update|autoremove)$/,
  zypper: /^(install|in|remove|rm|update|up|dup)$/,
  apk: /^(add|del|upgrade|update)$/,
  snap: /^(install|remove|refresh|revert|disable|enable|set)$/,
  flatpak: /^(install|uninstall|update|override)$/,
  port: /^(install|uninstall|upgrade|selfupdate)$/,
  choco: /^(install|uninstall|upgrade)$/,
  winget: /^(install|uninstall|upgrade|import)$/,
  scoop: /^(install|uninstall|update|reset)$/,
};

function killsWidely(args: string[]): boolean {
  let targets = args;
  if (has(args.slice(0, 1), '-s', '-n')) targets = args.slice(2);
  else if (args.length > 1 && /^-([A-Za-z][A-Za-z0-9]*|\d+)$/.test(args[0])) targets = args.slice(1);
  return targets.some((a) => a === '-1' || a === '0');
}

// `npm install -g` and its relatives. What follows -- is a script's own.
function nodePackagesOut(name: string, args: string[]): boolean {
  const end = args.indexOf('--');
  const own = end < 0 ? args : args.slice(0, end);
  const words = plain(own);
  const verb = words[0] ?? '';
  if (name === 'yarn' && verb === 'global') return has(words, 'add', 'remove', 'upgrade');
  if (['config', 'c'].includes(verb) && has(words.slice(1, 2), 'set', 'delete', 'edit')) {
    const project = own.some((a, i) => a === '--location=project' || (a === '--location' && own[i + 1] === 'project'));
    return !project && !has(own, '--workspace');
  }
  if (name === 'npm' && (verb === 'login' || verb === 'adduser' || verb === 'logout' || verb === 'set')) return true;
  if (verb === 'link' && words.length === 1 && name !== 'yarn') return true;
  if (['run', 'run-script', 'test', 't', 'exec', 'x'].includes(verb)) return false;
  const global = own.some((a, i) =>
    a === '--global' || a === '--global=true' || a === '--location=global' ||
    (a === '--location' && own[i + 1] === 'global') || /^-[A-Za-z]*g[A-Za-z]*$/.test(a));
  return global && words.some((w) =>
    /^(install|i|add|in|uninstall|remove|rm|un|update|up|upgrade|link|ci)$/.test(w));
}

// reachesOut is a command that changes this computer outside the project
// without having to name a path there.
export function reachesOut(name: string, args: string[], pickedBroadly: boolean): boolean {
  const words = plain(args);
  const lower = args.map((a) => a.toLowerCase());
  const verb = words[0] ?? '';
  const help = has(args, '--help', '--version');
  if (help) return false;

  if (/^pip[0-9.]*$/.test(name)) {
    if (verb === 'config') return has(words, 'set', 'unset', 'edit') && !has(args, '--site');
    return has(words, 'install', 'uninstall') && has(args, '--user', '--break-system-packages');
  }
  const system = Object.prototype.hasOwnProperty.call(SYSTEM_PACKAGES, name) ? SYSTEM_PACKAGES[name] : undefined;
  if (system) return words.some((w, i) => i < 2 && system.test(w));

  switch (name) {
    case 'kill':
      if (lower.includes('-name')) return words.some(broadPattern);
      return pickedBroadly || killsWidely(args);
    case 'pkill':
    case 'killall':
      return args.some((a) => /^(-[uU]|--user|--euid|--uid)/.test(a)) || words.slice(-1).some(broadPattern);
    case 'stop-process':
    case 'spps':
      return lower.some((a) => /^-(n|name|processname)$/.test(a)) && words.some(broadPattern);
    case 'taskkill': {
      const at = lower.indexOf('/im');
      return lower.includes('/fi') || (at >= 0 && broadPattern(args[at + 1] ?? ''));
    }

    case 'shutdown':
    case 'reboot':
    case 'poweroff':
    case 'halt':
    case 'pkexec':
    case 'gksudo':
    case 'chsh':
    case 'usermod':
    case 'useradd':
    case 'userdel':
    case 'adduser':
    case 'passwd':
    case 'visudo':
    case 'systemd-run':
    case 'setx':
    case 'osascript':
    case 'stop-computer':
    case 'restart-computer':
    case 'stop-service':
    case 'start-service':
    case 'restart-service':
    case 'new-service':
    case 'set-executionpolicy':
    case 'install-module':
    case 'register-scheduledtask':
      return true;
    case 'systemctl':
      return verb !== '' && !/^(status|is-active|is-enabled|is-failed|show|cat|help|list-[a-z-]+)$/.test(verb);
    case 'service':
      return words.length > 1 && words[1] !== 'status';
    case 'launchctl':
      return verb !== '' && !has([verb], 'list', 'print', 'help');
    case 'loginctl':
      return verb !== '' && !/^(list|show|.*-status$)/.test(verb);
    case 'crontab':
      return !has(args, '-l');
    case 'at':
      return !has(args, '-l');
    case 'schtasks':
      return args.length > 0 && !lower.includes('/query');
    case 'sc':
      return args.length > 0 && !/^(query|queryex|qc)$/i.test(verb);
    case 'net':
      return /^(start|stop|use|user|localgroup|share)$/i.test(verb) && args.length > 1;
    case 'reg':
      return args.length > 0 && !/^query$/i.test(verb);
    case 'set-itemproperty':
    case 'remove-itemproperty':
    case 'new-itemproperty':
      return lower.some((a) => /^(hklm|hkcu|hkey_|registry::)/.test(a));
    case 'gsettings':
      return /^(set|reset|reset-recursively)$/.test(verb);
    case 'dconf':
      return /^(write|reset|load)$/.test(verb);
    case 'defaults':
      return /^(write|delete|import|rename)$/.test(verb);
    case 'xdg-settings':
      return verb === 'set';
    case 'xdg-mime':
      return verb === 'default' || verb === 'install';
    case 'sysctl':
      return has(args, '-w', '--write', '-p') || words.some((w) => w.includes('='));
    case 'hostnamectl':
    case 'timedatectl':
      return verb.startsWith('set');
    case 'nmcli':
      return has(words, 'up', 'down', 'modify', 'delete', 'connect', 'disconnect', 'off');
    case 'iptables':
    case 'ufw':
      return args.length > 0 && !has(args, '-L', '--list', '-S', 'status');
    case 'mount':
    case 'umount':
      return words.length > 0;
    case 'update-alternatives':
      return has(args, '--install', '--set', '--remove', '--config');
    case 'pacman':
      return args.some((a) => /^-[A-Za-z]*[SRU]/.test(a) && !/^-S[si]/.test(a));
    case 'dpkg':
      return has(args, '-i', '--install', '-r', '--remove', '-P', '--purge');

    case 'npm':
    case 'pnpm':
    case 'yarn':
    case 'bun':
      return nodePackagesOut(name, args);
    case 'nix-env':
      return args.some((a) => /^-[a-z]*[ieu]|^--(install|uninstall|upgrade)/.test(a));
    case 'nix':
      return verb === 'profile' && has(words.slice(1, 2), 'install', 'add', 'remove', 'upgrade');
    case 'history':
      return has(args, '-c', '-w', '-d');
    case 'pipx':
      return /^(install|install-all|inject|upgrade|upgrade-all|uninstall|uninstall-all|ensurepath)$/.test(verb);
    case 'uv':
      return (verb === 'tool' && has(words.slice(1, 2), 'install', 'upgrade', 'uninstall', 'update-shell'))
        || (verb === 'pip' && has(args, '--system', '--break-system-packages'));
    case 'poetry':
      return verb === 'config' && !has(args, '--local', '--list') && (words.length > 2 || has(args, '--unset'));
    case 'conda':
    case 'mamba':
      if (verb === 'config') return has(args, '--set', '--add', '--remove', '--prepend', '--append');
      return /^(install|remove|update)$/.test(verb) && has(args, 'base');
    case 'cargo':
      return has(words.slice(0, 2), 'install', 'uninstall') && !args.some((a) => a.startsWith('--root'));
    case 'rustup':
      return /^(default|install|update|uninstall|set)$/.test(verb) || has(words.slice(1, 2), 'install', 'add', 'uninstall', 'remove');
    case 'gem':
      return /^(install|uninstall|update)$/.test(verb) && !has(args, '--install-dir', '-i');
    case 'go':
      return verb === 'install' || (verb === 'env' && has(args, '-w', '-u'));
    case 'dotnet':
      return verb === 'tool' && has(words, 'install', 'update', 'uninstall') && has(args, '-g', '--global');
    case 'composer':
      return verb === 'global' && !has(words.slice(1, 2), 'show', 'outdated', 'config');
    case 'corepack':
      return /^(enable|disable|install|up|use)$/.test(verb) || has(args, '--activate', '-g');
    case 'volta':
      return verb === 'install' || verb === 'uninstall';
    case 'asdf':
      return verb === 'global' || verb === 'install';
    case 'mise':
      return verb === 'use' && has(args, '-g', '--global');
    case 'code':
      return args.some((a) => /^--(un)?install-extension/.test(a));
    case 'docker':
    case 'podman':
      return verb === 'login' || verb === 'logout' || (verb === 'context' && has(words, 'use', 'create', 'rm'));
  }
  return false;
}

// gitReachesOut is git writing the user's own settings or stored logins.
export function gitReachesOut(sub: string, rest: string[]): boolean {
  if (sub === 'config') return has(rest, '--global', '--system') && gitConfigWrites(rest);
  if (sub === 'lfs') return has(rest.slice(0, 1), 'install', 'uninstall') && !has(rest, '--local');
  if (sub === 'maintenance') return has(rest.slice(0, 1), 'start', 'stop', 'register', 'unregister');
  return sub === 'credential';
}

function gitConfigWrites(rest: string[]): boolean {
  if (has(rest, '--unset', '--unset-all', '--add', '--replace-all', '-e', '--edit', '--remove-section', '--rename-section')) return true;
  if (rest.some((a) => /^(--get|--list$|-l$)/.test(a))) return false;
  const words = rest.filter((a, i) => !a.startsWith('-') && !['--file', '-f', '--type', '--default'].includes(rest[i - 1]));
  if (has(words.slice(0, 1), 'get', 'list')) return false;
  if (has(words.slice(0, 1), 'set', 'unset', 'edit', 'remove-section', 'rename-section')) return true;
  return words.length > 1;
}

// ---------------------------------------------------------------------------
// Sends

// A deploy tool asks unless the words are one of its local or reading commands:
// what changes a live service has too many names to list.
const LIVE_SERVICE_READS: Record<string, string[]> = {
  vercel: ['dev', 'build', 'list', 'ls', 'inspect', 'logs', 'whoami', 'help', 'init', 'link', 'pull', 'env ls', 'env list', 'env pull', 'project ls', 'domains ls'],
  netlify: ['dev', 'build', 'status', 'logs', 'help', 'link', 'env:list', 'env:get', 'sites:list', 'functions:serve', 'functions:list', 'functions:create', 'functions:invoke'],
  wrangler: ['dev', 'types', 'whoami', 'tail', 'init', 'build', 'check', 'docs', 'versions list', 'deployments list', 'kv namespace list', 'd1 list', 'd1 migrations list', 'd1 migrations create', 'pages dev', 'secret list'],
  firebase: ['emulators:start', 'emulators:exec', 'serve', 'help', 'use', 'projects:list', 'apps:list', 'login:list', 'functions:log', 'functions:shell', 'functions:list'],
  fly: ['status', 'logs', 'help', 'version', 'doctor', 'apps list', 'releases', 'config show', 'config validate', 'secrets list', 'machine list', 'machines list'],
  heroku: ['local', 'logs', 'help', 'version', 'apps', 'apps:info', 'ps', 'config', 'config:get', 'releases', 'releases:info', 'auth:whoami', 'addons', 'pg:info'],
  eas: ['whoami', 'config', 'help', 'diagnostics', 'build:list', 'build:view'],
  railway: ['status', 'logs', 'whoami', 'help', 'list', 'docs'],
  serverless: ['offline', 'package', 'print', 'info', 'logs', 'help'],
  supabase: ['start', 'stop', 'status', 'init', 'help', 'db diff', 'db start', 'db lint', 'db dump', 'db reset', 'gen', 'test', 'migration new', 'migration list', 'functions serve', 'functions new', 'functions list', 'projects list', 'secrets list'],
};
const SAME_TOOL: Record<string, string> = { flyctl: 'fly', sls: 'serverless' };

export function changesLiveService(name: string, args: string[]): boolean {
  if (has(args, '--help', '--version', '-h')) return false;
  const words = plain(args);
  const tool = Object.prototype.hasOwnProperty.call(SAME_TOOL, name) ? SAME_TOOL[name] : name;
  if (Object.prototype.hasOwnProperty.call(LIVE_SERVICE_READS, tool)) {
    if (has(args, '--prod')) return true;
    if (words.length === 0) return tool === 'vercel'; // bare vercel deploys
    if (tool === 'wrangler' && /^(d1|kv|r2)$/.test(words[0]) && has(args, '--local') && !has(args, '--remote')) return false;
    const said = [1, 2, 3].map((n) => words.slice(0, n).join(' '));
    return !said.some((s) => LIVE_SERVICE_READS[tool].includes(s));
  }
  switch (tool) {
    case 'eb':
      return has(words.slice(0, 1), 'deploy', 'create', 'terminate', 'setenv', 'scale', 'swap');
    case 'surge':
    case 'gh-pages':
      return true;
    case 'ansible-playbook':
      return !has(args, '--check', '-C', '--syntax-check', '--list-tasks', '--list-hosts');
    case 'sam':
    case 'cdk':
    case 'amplify':
    case 'modal':
    case 'skaffold':
    case 'cap':
    case 'dokku':
    case 'expo':
      return has(words.slice(0, 2), 'deploy', 'destroy', 'bootstrap', 'push', 'publish', 'delete', 'sync', 'run') && !(tool === 'expo' && words[0] === 'run');
    case 'huggingface-cli':
    case 'hf':
      return has(words.slice(0, 2), 'upload', 'upload-large-folder', 'create', 'delete');
    case 'rclone':
      return /^(copy|copyto|sync|bisync|move|moveto|delete|purge|rcat)$/.test(words[0] ?? '') && words.some((w) => /^[\w-]{2,}:/.test(w));
    case 's3cmd':
      return /^(put|sync|del|rm|mb|rb)$/.test(words[0] ?? '');
  }
  return false;
}

// runsDeployScript is a script whose own name says it deploys.
export function runsDeployScript(program: string, args: string[]): boolean {
  const deploys = (word: string): boolean => {
    const base = (word.split(/[/\\]/).pop() ?? '').toLowerCase();
    return /^(deploy|publish|release)(\.(sh|py|js|ts|mjs|cjs|rb|ps1|bat|cmd))?$/.test(base)
      || /^(deploy|publish)[-_][\w-]*\.(sh|py|js|ts|mjs|cjs|rb|ps1|bat|cmd)$/.test(base);
  };
  if (/[/\\]/.test(program) && deploys(program)) return true;
  const name = toolName(program);
  if (!/^(sh|bash|zsh|dash|ksh|python[0-9.]*|py|node|ruby|powershell|pwsh|source|\.)$/.test(name)) return false;
  const script = plain(args)[0] ?? '';
  return /[/\\.]/.test(script) && deploys(script);
}

const CODE_POST = /\b(requests|httpx|axios|got)\s*\.\s*(post|put|patch|delete)\s*\(|(?<![.\w])fetch\s*\([^]*method['"]?\s*:\s*['"](post|put|patch|delete)/i;

// codeSends is typed code that posts to an address on another computer.
export function codeSends(code: string): boolean {
  if (!CODE_POST.test(code)) return false;
  for (const m of code.matchAll(/https?:\/\/([^/'"`\s?#:]+|\[::1\])/gi)) {
    const host = m[1].slice(m[1].lastIndexOf('@') + 1);
    if (!/^(localhost|127(\.\d+){3}|0\.0\.0\.0|\[::1\])$/i.test(host)) return true;
  }
  return false;
}

// ---------------------------------------------------------------------------
// Unclear: the line hides what would run

// git's own commands. Any other word is an alias or a program called git-<word>,
// and either can be a push.
const GIT_COMMANDS = new Set((
  'add am annotate apply archive bisect blame branch bugreport bundle cat-file check-attr check-ignore check-mailmap ' +
  'check-ref-format checkout checkout-index cherry cherry-pick clean clone column commit commit-graph commit-tree config ' +
  'count-objects credential describe diagnose diff diff-files diff-index diff-tree difftool fast-export fast-import fetch ' +
  'filter-branch filter-repo for-each-ref format-patch fsck gc gitk grep gui hash-object help http-push index-pack init ' +
  'init-db interpret-trailers lfs log ls-files ls-remote ls-tree mailinfo mailsplit maintenance merge merge-base merge-file ' +
  'merge-tree mergetool mktag mktree mv name-rev notes pack-objects pack-refs patch-id prune prune-packed pull push ' +
  'range-diff read-tree rebase reflog remote repack replace request-pull rerere reset restore rev-list rev-parse revert rm ' +
  'send-email send-pack shortlog show show-branch show-index show-ref sparse-checkout stash status stripspace submodule ' +
  'subtree svn switch symbolic-ref tag unpack-objects update-index update-ref var verify-commit verify-pack verify-tag ' +
  'version whatchanged worktree write-tree'
).split(' '));

export function gitHides(sub: string, rest: string[], fedByXargs: boolean): boolean {
  if (sub === '') return fedByXargs;
  if (!GIT_COMMANDS.has(sub)) return true;
  return sub === 'config' && gitConfigWrites(rest) && rest.some((a) => /^alias\./i.test(a));
}

const VERB_IN_LINE = new Set(['npm', 'pnpm', 'yarn', 'bun', 'cargo', 'docker', 'podman', 'kubectl', 'helm', 'twine', 'gem', 'terraform']);

// hidesWhatRuns is a program fed what it will run, or told it in a variable.
export function hidesWhatRuns(name: string, args: string[], piped: boolean, hasBody: boolean, fedByXargs: boolean): boolean {
  if (name.includes('{}')) return true;
  if (name === 'iex' || name === 'invoke-expression') return true;
  const words = plain(args);
  if (VERB_IN_LINE.has(name)) {
    if (fedByXargs && words.length === 0) return true;
    // A script's name is npm's second word; after `docker run` come option values.
    const script = words[0] === 'run' && ['npm', 'pnpm', 'yarn', 'bun'].includes(name);
    const verb = words[script ? 1 : 0] ?? '';
    if (/^\$|`|^%.*%$/.test(verb)) return true;
  }
  if (!piped || hasBody) return false;
  if (name === 'cmd') return !args.some((a) => /^\/[ck]$/i.test(a));
  if (name === 'source' || name === '.') return words.some((w) => w === '/dev/stdin' || w === '-');
  if (!/^(python[0-9.]*|py|node|deno|bun|ruby|perl|php)$/.test(name)) return false;
  if (args.some((a) => /^(-[a-z]*[cep]|-m|--eval|--print|--version|-V)$/.test(a))) return false;
  return words.length === 0 || words[0] === '/dev/stdin';
}
