// Minimal LuCI/DOM stand-ins; no browser packages needed for regression checks.
const assert = require('node:assert/strict');
const fs = require('node:fs');
String.prototype.format = function(value) { return this.replace('%s', value); };

function E(tag, attrs = {}, children = []) {
	return {
		tag, attrs, children: Array.isArray(children) ? children : [children], style: { setProperty(name, value) { this[name] = value; } },
		scrollTop: 0, scrollHeight: 1000, clientHeight: 400,
		getBoundingClientRect() { return { top: 140 }; },
		get childNodes() { return this.children; },
		get textContent() { return this.text ?? this.children.map(c => typeof c === 'object' ? c.textContent : c).join(''); },
		set textContent(value) { this.text = value; },
		querySelectorAll(tag) {
			const matches = [];
			function visit(node) { for (const child of node.children || []) if (typeof child === 'object') { if (child.tag === tag) matches.push(child); visit(child); } }
			visit(this);
			return matches;
		},
		querySelector(selector) {
			assert.equal(selector, '.cbi-tabmenu [data-tab="settings"] a');
			return { click: () => tabs[1].attrs['cbi-tab-active']() };
		},
		removeAttribute(name) { delete this.attrs[name]; }, focus() {}, select() {}
	};
}
function find(node, predicate) {
	if (predicate(node)) return node;
	for (const c of node.children || []) {
		if (typeof c === 'object') { const match = find(c, predicate); if (match) return match; }
	}
}

const tasks = new Set(), calls = [];
const account = { id: 'a', email: 'a@example.com', enabled: true, usage: { limit_bytes: 10e9, remaining_bytes: 7.5e9 } };
let modal, tabs, nextStep = 'working', readOnly = false;
let serviceEnabled = true;
let stoppedStatus;
let lanIP = '192.168.1.1';
let pauseAction;
let failedRefreshID;
let savedSettings = {};
const notifications = [], clipboardWrites = [];
const browserWindow = { location: { hostname: 'router.lan' }, requestAnimationFrame: fn => fn(), innerHeight: 800 };
const browserNavigator = {};
const logs = [{ time: 'old', level: 'INFO', message: 'first' }, { time: 'new', level: 'INFO', message: 'latest' }];
let status = { version: '0.1.0-r16', state: { phase: 'running', message: 'Proxy running' }, proxy: { listen: '0.0.0.0:1088', started_at: 'now' } };
const ui = {
	showModal: (title, content) => { modal = E('div', {}, content); }, hideModal: () => { modal = null; },
	addNotification: (_, error, type) => { notifications.push(type); if (type === 'error') throw Error(error.textContent); },
	tabs: { initTabGroup: panes => { tabs = panes; } }
};
const poll = { add: fn => tasks.add(fn), remove: fn => tasks.delete(fn) };
const rpc = { declare: spec => async (action, id, data) => {
	if (spec.object === 'network.interface.lan') return lanIP ? { 'ipv4-address': [{ address: lanIP }] } : {};
	calls.push({ method: spec.method, action, id, data });
	if (pauseAction && action === pauseAction.action) await pauseAction.wait;
	if (action === 'account_refresh' && id === failedRefreshID) return { ok: false, error: 'Quota refresh failed' };
	if (action === 'status' && !status) return { ok: false, error: 'Service stopped' };
	if (action === 'service_enable') serviceEnabled = JSON.parse(data).enabled;
	if (action === 'stop_service') { stoppedStatus = status; status = null; }
	if (action === 'restart_service') status = stoppedStatus || status;
	if (spec.method === 'write' && action === 'settings') savedSettings = JSON.parse(data);
	return { ok: true, data: action === 'status' ? status : action === 'signin' || action === 'signin_answer' ?
		{ id: 'flow', step: nextStep, message: nextStep } : action === 'accounts' ? { accounts: [account] } :
		action === 'connection_links' ? { socks5: 'socks5://user:p%40ss@0.0.0.0:1088', http: 'http://user:p%40ss@0.0.0.0:1088' } :
		action === 'service' ? { enabled: serviceEnabled } : action === 'logs' ? { entries: logs } : action === 'settings' ? { ...savedSettings } : action === 'locations' ? { locations: [{ code: 'NL', name: 'Netherlands' }, { code: 'DE', name: 'Germany', locked: true }] } : {} };
} };
let mapReadOnly, settingsData, formSections;
const choices = [];
const options = [];
const optionDetails = {};
const form = { ListValue: 'list', Value: 'value', JSONMap: function(data, title) {
	assert.equal(title, 'Settings', 'keep the Settings header above the category subheadings');
	settingsData = data;
	const sections = formSections = [];
	this.data = { get: (config, section, key) => data[section][key] };
	this.save = callback => callback();
	this.section = (_, name, type, title) => {
		const section = { name, type, title, options: [], option: (type, name, label, description) => {
			options.push(name);
			section.options.push(name);
			return optionDetails[name] = { type, label, description, value(code, label) { choices.push({ name, code, label }); } };
		} };
		sections.push(section);
		return section;
	};
	this.render = async () => { mapReadOnly = this.readonly; return E('form', {}, [E('h2', {}, [title]), ...sections.map(s => E('section', { 'class': 'cbi-section' }, [E('h3', {}, [s.title])]))]); };
} };
const source = fs.readFileSync('luci-app-firefox-vpn/htdocs/luci-static/resources/view/firefox-vpn/overview.js', 'utf8');
const app = new Function('view', 'rpc', 'poll', 'ui', 'form', 'dom', 'E', '_', 'L', 'window', 'navigator', 'document', source)(
	{ extend: v => v }, rpc, poll, ui, form,
	{ content: (el, content) => { el.text = undefined; el.children = Array.isArray(content) ? content : [content]; } },
	E, s => s, { hasViewPermission: () => !readOnly, resource: path => '/luci-static/resources/' + path }, browserWindow, browserNavigator, { documentElement: { scrollHeight: 900 } }
);
const proxyURLs = () => app.linksNode.querySelectorAll('input').map(node => node.attrs.value).join('\n');

(async () => {
	const refresh = app.refresh;
	app.refresh = async () => {};
	const page = app.render({ status, lan: { 'ipv4-address': [{ address: lanIP }] } });
	assert.deepEqual(tabs.map(t => t.attrs['data-tab']), ['overview', 'settings', 'logs']);
	assert.ok(find(tabs[0], n => n.tag === 'h2' && n.textContent === 'Firefox VPN v0.1.0-r16'));
	assert.equal(find(page, n => n.tag === 'h2'), find(tabs[0], n => n.tag === 'h2'));
	assert.ok(!page.textContent.includes('SOCKS5 and HTTP share one port'));
	assert.ok(!page.textContent.includes('Connect and Disconnect apply'));
	const sections = tabs[0].querySelectorAll('section');
	assert.equal(sections.length, 3, 'use the reference design’s three native LuCI sections');
	assert.ok(!find(tabs[0], n => n.attrs.class?.includes('ffvpn-panel')), 'Overview has no custom card borders');
	assert.deepEqual(tabs[0].querySelectorAll('h3').map(n => n.textContent), ['Status', 'Proxy access', 'Accounts']);
	assert.deepEqual(sections[0].querySelectorAll('button').map(n => n.textContent), ['Restart', 'Stop', 'Enable', 'Disable']);
	assert.ok(find(sections[0], n => n.attrs.class === 'ffvpn-controls' && n.children.includes(app.restartServiceButton) && n.children.includes(app.disableButton)), 'keep all service buttons in one wrapping group');
	assert.ok(!page.textContent.includes('Restart proxy'), 'service restart is sufficient for recovery');
	assert.ok(find(page, n => n.tag === 'link' && n.attrs.href.endsWith('overview.css?v=23')), 'version stylesheet to avoid stale layouts after upgrades');
	assert.equal(find(tabs[0], n => n.tag === 'h2').children[2], app.versionNode, 'small version sits beside the unchanged heading');
	assert.equal(app.versionNode.tag, 'small');
	assert.equal(app.versionNode.attrs.class, 'ffvpn-version');
	assert.ok(!app.stateNode.textContent.includes('Version'), 'do not duplicate the version in the State row');
	app.renderStatus({ ...status, version: '0.1.0-r17' });
	assert.equal(app.versionNode.textContent, 'v0.1.0-r17', 'refresh the version from the backend, not a hardcoded UI label');
	app.renderStatus(null);
	assert.equal(app.versionNode.textContent, 'v0.1.0-r17', 'keep the last known version visible when stopping the service');
	app.renderStatus(status);
	const github = find(tabs[0], n => n.tag === 'a' && n.textContent === 'GitHub');
	assert.equal(github.attrs.href, 'https://github.com/BercX/firefox-vpn-openwrt');
	assert.equal(github.attrs.rel, 'noopener noreferrer');
	assert.deepEqual(find(sections[2], n => n.attrs.class === 'cbi-section-create').querySelectorAll('button').map(n => n.textContent), ['Add account', 'Refresh quota']);
	assert.ok(!tabs[0].textContent.includes('Sign in again'));
	assert.ok(find(app.statusNode, n => n.tag === 'table' && n.attrs['aria-labelledby'] === 'ffvpn-status-heading'));
	const stateRow = find(app.statusNode, n => n.tag === 'tr' && n.children[0]?.textContent === 'State');
	assert.equal(stateRow.children[1].children[0], app.stateNode);
	assert.deepEqual(stateRow.querySelectorAll('button'), [app.restartServiceButton, app.stopServiceButton, app.enableButton, app.disableButton]);
	assert.deepEqual(app.stateNode.children.map(n => n.textContent), ['Service: Running', 'Boot startup: Unknown', 'Proxy: Running']);
	assert.ok(!find(app.statusNode, n => n.tag === 'th' && ['Service', 'Upstream'].includes(n.textContent)), 'no separate Service or Upstream row');
	assert.ok(find(app.statusNode, n => n.tag === 'th' && n.attrs.scope === 'row' && n.textContent === 'Public IPv4'));
	assert.ok(find(app.statusNode, n => n.tag === 'th' && n.attrs.scope === 'row' && n.textContent === 'Public IPv6'));
	assert.ok(!app.statusNode.textContent.includes('Last connection test'), 'no separate test tile');
	assert.equal(sections[2].children[1], app.trafficNode, 'quota summary sits below the Accounts heading');
	assert.ok(find(app.linksNode, n => n.tag === 'table' && n.attrs.class.includes('cbi-section-table')), 'proxy URLs use native LuCI table rows');
	assert.ok(find(app.statusNode, n => n.textContent === 'Running' && n.attrs.class === 'ffvpn-good'));
	for (const [phase, label] of [['starting', 'Starting'], ['waiting', 'Waiting'], ['error', 'Error']]) {
		app.renderStatus({ ...status, state: { phase } });
		assert.equal(app.stateNode.children[0].textContent, 'Service: Running');
		assert.equal(app.stateNode.children[2].textContent, 'Proxy: ' + label);
	}
	app.renderStatus({ ...status, proxy: { ...status.proxy, upstream: 'upstream.example:443' } });
	assert.ok(!app.statusNode.textContent.includes('upstream.example'), 'do not display the upstream address');
	app.renderStatus(status);
	assert.ok(find(tabs[1], n => n === app.settingsNode));
	assert.ok(find(tabs[2], n => n === app.logsNode));
	assert.match(proxyURLs(), /socks5:\/\/192.168.1.1:1088/);
	assert.match(proxyURLs(), /http:\/\/192.168.1.1:1088/);
	assert.ok(!app.linksNode.textContent.includes('Listener'));
	assert.ok(!app.linksNode.textContent.includes('Router LAN IP'));
	assert.ok(!app.linksNode.textContent.includes('Paste these URLs'));
	app.renderLinks({ listen: '[::]:1088' });
	assert.match(proxyURLs(), /192.168.1.1:1088/);
	app.renderLinks({ listen: '127.0.0.1:1088' });
	assert.match(proxyURLs(), /192.168.1.1:1088/);
	assert.match(app.linksNode.textContent, /listener is router-only/);
	assert.equal(find(app.linksNode, n => n.tag === 'button').attrs.disabled, true);
	assert.ok(find(app.linksNode, n => n.attrs.class?.includes('ffvpn-warning') && n.attrs.role === 'status'));
	find(app.linksNode, n => n.tag === 'a' && n.textContent === 'Open Settings').attrs.click({ preventDefault() {} });
	await new Promise(resolve => setImmediate(resolve));
	assert.equal(app.settingsLoaded, true);
	app.renderLinks({ listen: '192.168.2.1:1088' });
	assert.match(proxyURLs(), /192.168.2.1:1088/, 'preserve an explicitly bound address');
	app.renderAccounts([account]);
	assert.match(app.trafficNode.textContent, /7.50 GB/);
	app.renderAccounts([account, { ...account, id: 'b', usage: { limit_bytes: 20e9, remaining_bytes: 8e9 } }, { ...account, id: 'disabled', enabled: false, usage: { unlimited: true } }]);
	assert.match(app.trafficNode.textContent, /15.50 GB/);
	assert.match(app.trafficNode.textContent, /2 enabled accounts/);
	assert.ok(!app.trafficNode.textContent.includes('0 unlimited'));
	assert.equal(app.trafficNode.textContent.split('15.50 GB').length - 1, 1, 'do not duplicate quota totals');
	app.renderAccounts([account, { ...account, id: 'u', usage: { unlimited: true } }, { ...account, id: 'unknown', usage: null }]);
	assert.match(app.trafficNode.textContent, /Unlimited/);
	assert.match(app.trafficNode.textContent, /1 unlimited, 1 unknown quota/);
	assert.match(app.trafficNode.textContent, /7.50 GB/);
	app.renderAccounts([{ ...account, usage: null }]);
	assert.match(app.trafficNode.textContent, /Unknown/);
	app.renderAccounts([]);
	assert.match(app.trafficNode.textContent, /0.00 GB/);
	assert.equal(app.quotaButton.disabled, true, 'no saved accounts to refresh');
	app.renderStatus({ ...status, state: { phase: 'off' } });
	app.renderAccounts([{ ...account, enabled: false }]);
	assert.equal(find(app.accountsNode, n => n.tag === 'button' && n.textContent === 'Use now').attrs.disabled, true);
	app.renderAccounts([account]);
	assert.deepEqual(app.accountsNode.querySelectorAll('button').map(node => node.textContent), ['Use now', 'Disable', 'Remove']);
	await find(app.accountsNode, n => n.tag === 'button' && n.textContent === 'Remove').attrs.click();
	assert.deepEqual(modal.querySelectorAll('button').map(node => node.textContent), ['Cancel', 'Remove']);
	assert.equal(calls.filter(c => c.action === 'account_remove').length, 0, 'removal needs confirmation');
	app.busy = true;
	app.updateButtons();
	assert.equal(find(modal, n => n.tag === 'button' && n.textContent === 'Remove').disabled, true);
	assert.equal(find(app.accountsNode, n => n.tag === 'button' && n.textContent === 'Disable').disabled, true);
	app.busy = false;
	app.updateButtons();
	assert.equal(find(modal, n => n.tag === 'button' && n.textContent === 'Remove').disabled, false);
	await find(modal, n => n.tag === 'button' && n.textContent === 'Cancel').attrs.click();
	assert.equal(modal, null);
	assert.match(app.accountsNode.textContent, /7.50 \/ 10.00 GB \(75.0%\)/, 'quota column reports remaining traffic, not used traffic');
	assert.deepEqual(app.accountsNode.querySelectorAll('th').map(n => n.textContent), ['Account', 'Status', 'Quota remaining', 'Actions']);
	assert.ok(app.accountsNode.querySelectorAll('th').every(n => n.attrs.scope === 'col'));
	assert.ok(app.accountsNode.querySelectorAll('td').every(n => n.attrs['data-title'] || n.attrs.class.includes('cbi-section-actions')), 'keep mobile table labels');
	const batchStart = calls.length, refreshBeforeBatch = app.refresh;
	let totalsUpdated = false;
	app.refresh = async () => { totalsUpdated = true; };
	app.renderAccounts([account, { ...account, id: 'bad', email: 'bad@example.com' }, { ...account, id: 'disabled', enabled: false }]);
	failedRefreshID = 'bad';
	await assert.rejects(app.refreshQuotas(), /bad@example.com: Quota refresh failed/);
	assert.deepEqual(calls.slice(batchStart).filter(c => c.action === 'account_refresh').map(c => c.id), ['a', 'bad', 'disabled'], 'refresh all saved accounts, continuing after a failure');
	assert.equal(totalsUpdated, true, 'update totals even after a partial failure');
	failedRefreshID = null;
	app.refresh = refreshBeforeBatch;
	app.renderAccounts([account]);
	assert.ok(find(app.accountsNode, n => n.attrs.class === 'ffvpn-good'));
	app.renderAccounts([{ ...account, status: 'ready' }]);
	assert.match(app.accountsNode.textContent, /Ready/, 'human-readable account status');
	for (const [remaining, tone] of [[2e9, 'warning'], [0, 'bad']]) {
		app.renderAccounts([{ ...account, usage: { limit_bytes: 10e9, remaining_bytes: remaining } }]);
		assert.ok(find(app.accountsNode, n => n.attrs.class === 'ffvpn-' + tone));
	}
	app.renderStatus({ ...status, state: { phase: 'error', message: 'Cannot bind' } });
	assert.ok(find(app.statusNode, n => n.attrs.class === 'ffvpn-bad' && n.textContent === 'Cannot bind'));
	app.renderStatus({ ...status, state: { phase: 'off' } });
	assert.ok(find(app.statusNode, n => n.attrs.class === 'ffvpn-bad' && n.textContent === 'Disabled'));
	assert.match(app.statusNode.textContent, /Restart the service to check the public IP/);
	app.renderStatus({ ...status, testing: true });
	assert.match(app.statusNode.textContent, /Checking through the proxy/);
	app.renderStatus({ ...status, testing: true, exit: { ipv4: '203.0.113.7' } });
	assert.match(app.statusNode.textContent, /203.0.113.7/, 'keep known IP visible during automatic rechecks');
	app.renderStatus({ ...status, last_test: { exit_error: 'IP probe refused' } });
	assert.match(app.statusNode.textContent, /IP probe refused/);
	assert.ok(find(app.statusNode, n => n.attrs.class === 'ffvpn-bad' && n.textContent === 'IP probe refused'));
	app.renderStatus({ ...status, last_test: { ok: true } });
	assert.ok(!app.statusNode.textContent.includes('Passed'));
	assert.equal(app.statusNode.querySelectorAll('button').length, 4, 'only the State row has controls');
	app.renderStatus({ ...status, exit: { ipv4: '203.0.113.7', country_code: 'nl' }, proxy: { ...status.proxy, country_name: 'Recomended Location', country_code: 'REC' } });
	assert.match(app.statusNode.textContent, /203.0.113.7/);
	assert.match(app.statusNode.textContent, /Recommended Location \(🇳🇱 NL\)/);
	const nativeFlag = find(app.statusNode, n => n.attrs.class === 'ffvpn-flag');
	assert.equal(nativeFlag.textContent, '🇳🇱', 'NL must map to the Netherlands regional-indicator pair');
	assert.equal(nativeFlag.attrs['aria-hidden'], 'true', 'country code supplies the accessible text');
	assert.equal(app.statusNode.querySelectorAll('img').length, 0, 'built-in flags need no image assets or downloads');
	app.renderStatus({ ...status, proxy: { ...status.proxy, country_code: 'de' } });
	assert.match(app.statusNode.textContent, /\(🇩🇪 DE\)/, 'use the proxy country before an exit-country check is available');
	app.renderStatus({ ...status, exit: { country_code: '../nl' }, proxy: { ...status.proxy, country_code: 'ZZ' } });
	assert.ok(!app.statusNode.textContent.includes('../nl') && !app.statusNode.textContent.includes('(ZZ)'), 'reject invalid and unknown country codes');
	app.renderStatus({ ...status, proxy: { ...status.proxy, country_code: 'REC' } });
	assert.ok(!find(app.statusNode, n => n.attrs.class === 'ffvpn-flag'), 'do not invent an exit country');
	await find(app.linksNode, n => n.tag === 'button').attrs.click({ currentTarget: {} });
	assert.match(modal.textContent, /Copy this URL/);
	ui.hideModal();
	app.renderLinks({ ...status.proxy, login_required: true });
	await find(app.linksNode, n => n.tag === 'button').attrs.click({ currentTarget: {} });
	assert.equal(find(modal, n => n.tag === 'input').attrs.value, 'socks5://user:p%40ss@192.168.1.1:1088');
	ui.hideModal();
	browserWindow.isSecureContext = true;
	browserNavigator.clipboard = { writeText: async value => { clipboardWrites.push(value); } };
	const beforeCopy = notifications.length;
	await find(app.linksNode, n => n.tag === 'button').attrs.click();
	assert.deepEqual(clipboardWrites, ['socks5://user:p%40ss@192.168.1.1:1088']);
	assert.equal(notifications.length, beforeCopy, 'copying a URL should not show a notification');
	assert.equal(modal, null, 'clipboard success needs no modal');

	app.followSignIn({ id: 'flow', step: 'working', message: 'Signing in' });
	const tick = [...tasks].at(-1);
	const submit = find(modal, n => n.tag === 'button' && n.textContent === 'Continue');
	assert.equal(submit.style.display, 'none', 'Continue must not be visible while working');
	nextStep = 'need_email_code';
	await tick();
	assert.equal(submit.style.display, '');
	nextStep = 'working';
	await submit.attrs.click({ currentTarget: submit });
	assert.equal(submit.style.display, 'none');
	nextStep = 'done';
	await tick();
	assert.equal(modal, null, 'successful sign-in should close itself');
	assert.ok(!tasks.has(tick), 'completed sign-in must stop polling');
	app.followSignIn({ id: 'flow', step: 'failed', message: 'Invalid code', error: 'Try again' });
	assert.equal(find(modal, n => n.tag === 'button' && n.textContent === 'Continue').style.display, 'none');
	assert.equal(find(modal, n => n.tag === 'button' && n.textContent === 'Close').style.display, '');

	app.refresh = refresh;
	calls.length = 0;
	await app.refresh();
	assert.equal(app.enableButton.disabled, true, 'already enabled and running');
	assert.deepEqual(app.stateNode.children.map(n => n.textContent), ['Service: Running', 'Boot startup: Enabled', 'Proxy: Running']);
	assert.equal(app.disableButton.disabled, false);
	assert.ok(app.logsNode.textContent.indexOf('first') < app.logsNode.textContent.indexOf('latest'), 'logs run oldest to newest');
	assert.equal(logs[0].message, 'first', 'do not mutate received log order');
	app.logsNode.scrollTop = 100;
	tabs[2].attrs['cbi-tab-active']();
	assert.equal(app.logsNode.scrollTop, app.logsNode.scrollHeight, 'reopening Logs scrolls to the bottom');
	assert.equal(app.logsNode.style['--ffvpn-logs-space'], '101px', 'reserve all surrounding page space, including the footer');
	assert.equal(app.logsNode.attrs.class, 'ffvpn-logs');
	assert.match(fs.readFileSync('luci-app-firefox-vpn/htdocs/luci-static/resources/view/firefox-vpn/overview.css', 'utf8'), /\.ffvpn \[data-tab="logs"\] \.ffvpn-panel, \.ffvpn \.ffvpn-logs \{ border: 0; border-radius: 0; box-shadow: none; \}/, 'remove only Logs panel and viewer borders');
	assert.equal(find(tabs[2], n => n.tag === 'h3').textContent, 'Logs');
	assert.equal(calls.filter(c => c.action === 'test').length, 1, 'new proxy needs a public-IP probe');
	await app.refresh();
	assert.equal(app.logsNode.scrollTop, app.logsNode.scrollHeight, 'follow updates when already at the bottom');
	app.logsNode.scrollTop = 100;
	await app.refresh();
	assert.equal(app.logsNode.scrollTop, 100, 'polling must not interrupt reading older entries');
	assert.equal(calls.filter(c => c.action === 'test').length, 1, 'polling must not restart the probe');
	await app.quotaButton.attrs.click();
	assert.equal(calls.filter(c => c.action === 'test').length, 2, 'quota refresh also triggers a public-IP probe');
	account.active = true;
	await app.refresh();
	assert.equal(find(app.accountsNode, n => n.tag === 'button' && n.textContent === 'Use now').attrs.disabled, true);
	account.active = false;
	let resume;
	pauseAction = { action: 'account_refresh', wait: new Promise(resolve => { resume = resolve; }) };
	const pendingButton = app.quotaButton;
	const pending = pendingButton.attrs.click({ currentTarget: pendingButton });
	await new Promise(resolve => setImmediate(resolve));
	assert.equal(app.disableButton.disabled, true, 'disable conflicting controls while an action is pending');
	assert.equal(app.stopServiceButton.disabled, true);
	assert.equal(app.restartServiceButton.disabled, true);
	assert.equal(app.quotaButton.disabled, true);
	await pendingButton.attrs.click({ currentTarget: pendingButton });
	assert.equal(calls.filter(c => c.action === 'account_refresh').length, 2, 'ignore duplicate clicks while pending');
	await app.refresh();
	assert.equal(app.stopServiceButton.disabled, true, 'polling must not unlock controls during an action');
	resume();
	await pending;
	pauseAction = null;
	assert.equal(app.stopServiceButton.disabled, false);
	assert.equal(app.enableButton.disabled, true, 'completion must preserve state-based disabling');
	lanIP = '192.168.8.1';
	await app.refresh();
	assert.match(proxyURLs(), /192.168.8.1:1088/);
	lanIP = null;
	await app.refresh();
	assert.match(proxyURLs(), /router.lan:1088/);
	assert.equal(app.stopServiceButton.disabled, false);
	await app.stopServiceButton.attrs.click();
	assert.equal(status, null);
	assert.equal(serviceEnabled, true, 'stop must preserve boot startup');
	assert.equal(app.stopServiceButton.disabled, true);
	assert.equal(app.restartServiceButton.disabled, false, 'restart can recover a stopped service');
	const stops = calls.filter(c => c.action === 'stop_service').length;
	await app.stopServiceButton.attrs.click();
	assert.equal(calls.filter(c => c.action === 'stop_service').length, stops);
	await app.restartServiceButton.attrs.click();
	assert.ok(status);
	assert.equal(serviceEnabled, true, 'restart must preserve boot startup');
	assert.equal(app.stopServiceButton.disabled, false);
	const running = status;
	status = null;
	await app.disableButton.attrs.click({ currentTarget: {} });
	assert.equal(serviceEnabled, false);
	assert.deepEqual(app.stateNode.children.map(n => n.textContent), ['Service: Stopped', 'Boot startup: Disabled', 'Proxy: Unavailable']);
	assert.equal(app.disableButton.disabled, true);
	assert.equal(app.quotaButton.disabled, true, 'cannot refresh while service is stopped');
	assert.match(app.trafficNode.textContent, /unknown while the service is stopped/);
	assert.equal(app.statusNode.querySelectorAll('button').length, 4, 'keep recovery controls in the State row when stopped');
	await app.enableButton.attrs.click({ currentTarget: {} });
	assert.equal(serviceEnabled, true, 'enable must work even while the daemon is stopped');
	assert.deepEqual(app.stateNode.children.map(n => n.textContent), ['Service: Unavailable / failed to start', 'Boot startup: Enabled', 'Proxy: Unavailable']);
	assert.equal(app.enableButton.disabled, false, 'retry an enabled service that failed to start');
	status = running;
	savedSettings = { country: '', server: 'pinned.example:443', allow_locked: true, listen: '192.168.1.1:1088', username: 'proxy-user', dns_servers: ['https://1.1.1.1/dns-query'], dns_fallback: true, max_connections: 256, transport: 'h3', udp_mode: 'dns' };
	await app.settings();
	assert.equal(settingsData.logging.log_level, 'info', 'default logging level is Info');
	assert.equal(settingsData.connection.country, 'recommended', 'show Recommended rather than an empty location');
	assert.equal(optionDetails.country.type, form.ListValue, 'location must be a fixed selection, not free text');
	assert.equal(optionDetails.country.rmempty, false, 'do not allow an empty location');
	assert.equal(optionDetails.country.default, 'recommended');
	assert.ok(choices.filter(c => c.name === 'country').every(c => c.code !== ''), 'no blank location option');
	assert.deepEqual(app.settingsNode.querySelectorAll('button').map(n => n.textContent), ['Save and apply']);
	settingsData.logging.log_level = 'warn';
	await find(app.settingsNode, n => n.tag === 'button' && n.textContent === 'Save and apply').attrs.click();
	assert.equal(savedSettings.log_level, 'warn', 'send the selected log level through the settings RPC');
	assert.equal(settingsData.logging.log_level, 'warn', 'reload the saved log level');
	assert.equal(savedSettings.server, 'pinned.example:443', 'do not discard an existing pin when changing unrelated settings');
	assert.equal(savedSettings.allow_locked, true, 'preserve an existing advanced policy without showing its toggle');
	assert.equal(savedSettings.listen, '192.168.1.1:1088');
	assert.equal(savedSettings.username, 'proxy-user');
	assert.equal(savedSettings.password, '', 'leave an empty password for the backend to preserve');
	assert.deepEqual(savedSettings.dns_servers, ['https://1.1.1.1/dns-query']);
	assert.equal(savedSettings.dns_fallback, true);
	assert.equal(savedSettings.max_connections, 256);
	assert.equal(savedSettings.transport, 'h3');
	assert.equal(savedSettings.country, '', 'translate Recommended to the core representation');
	settingsData.logging.log_level = 'off';
	await find(app.settingsNode, n => n.tag === 'button' && n.textContent === 'Save and apply').attrs.click();
	assert.equal(savedSettings.log_level, 'off');
	assert.equal(settingsData.logging.log_level, 'off', 'keep Disable selected after saving');
	settingsData.connection.country = 'NL';
	await find(app.settingsNode, n => n.tag === 'button' && n.textContent === 'Save and apply').attrs.click();
	assert.equal(savedSettings.country, 'NL');
	assert.equal(savedSettings.server, '', 'a location change must clear a hidden conflicting exact-server pin');
	settingsData.connection.country = 'recommended';
	await find(app.settingsNode, n => n.tag === 'button' && n.textContent === 'Save and apply').attrs.click();
	assert.equal(savedSettings.country, '', 'allow returning to Recommended from a country');
	assert.equal(settingsData.connection.country, 'recommended');
	readOnly = true;
	app.render({ status });
	await app.settings();
	assert.equal(mapReadOnly, true, 'set form permissions before rendering');
	const netherlands = choices.find(c => c.code === 'NL');
	assert.equal(netherlands.label.textContent, '🇳🇱 NL: Netherlands', 'settings use the same native flags as Overview');
	assert.equal(find(netherlands.label, n => n.attrs.class === 'ffvpn-flag').textContent, '🇳🇱');
	assert.deepEqual([...new Set(choices.filter(c => c.name === 'udp_mode').map(c => c.code))], ['off', 'dns']);
	assert.deepEqual([...new Set(choices.filter(c => c.name === 'log_level').map(c => c.code))], ['off', 'error', 'warn', 'info', 'debug']);
	const saves = calls.filter(c => c.method === 'write' && c.action === 'settings').length;
	await find(app.settingsNode, n => n.tag === 'button' && n.textContent === 'Save and apply').attrs.click();
	assert.equal(calls.filter(c => c.method === 'write' && c.action === 'settings').length, saves, 'read-only users cannot change log levels');
	assert.ok(!options.includes('allow_locked') && !options.includes('server'), 'hide advanced location controls');
	assert.equal(optionDetails.password.label, 'Proxy Password');
	assert.match(optionDetails.password.description, /Leave empty to keep/);
	assert.equal(optionDetails.country.description, 'Mozilla may restrict access to locked locations.');
	assert.ok(choices.some(c => c.code === 'DE' && c.label.textContent.includes('(locked)')), 'keep locked locations visibly marked');
	assert.deepEqual(formSections.map(s => [s.name, s.title, s.options]), [
		['access', 'Proxy access', ['listen', 'username', 'password']],
		['connection', 'Connection', ['country', 'transport', 'udp_mode', 'max_connections']],
		['dns', 'DNS', ['dns_servers', 'dns_fallback']],
		['logging', 'Logging', ['log_level']]
	]);
	assert.equal(app.settingsNode.attrs.class, 'ffvpn-settings');
	assert.equal(find(app.settingsNode, n => n.tag === 'h2').textContent, 'Settings');
	assert.deepEqual(app.settingsNode.querySelectorAll('h3').map(n => n.textContent), ['Proxy access', 'Connection', 'DNS', 'Logging']);
	for (const control of [app.restartServiceButton, app.stopServiceButton, app.enableButton, app.disableButton, app.quotaButton]) assert.equal(control.disabled, true);
	console.log('LuCI regression checks passed (grouped controls, pending/disabled actions, total quotas, LAN URLs, logs, sign-in, permissions).');
})().catch(err => { console.error(err); process.exitCode = 1; });
