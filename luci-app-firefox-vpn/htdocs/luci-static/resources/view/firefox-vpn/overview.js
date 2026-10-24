'use strict';
'require view';
'require rpc';
'require poll';
'require ui';
'require form';
'require dom';

var readRPC = rpc.declare({
	object: 'luci.firefox-vpn', method: 'read', params: ['action', 'id'],
	expect: { '': {} }
});
var writeRPC = rpc.declare({
	object: 'luci.firefox-vpn', method: 'write', params: ['action', 'id', 'data'],
	expect: { '': {} }
});
var lanStatus = rpc.declare({ object: 'network.interface.lan', method: 'status', expect: { '': {} } });

function unwrap(reply) {
	if (!reply.ok) throw new Error(reply.error || _('Firefox VPN request failed.'));
	return reply.data;
}
function read(action, id) { return readRPC(action, id || '').then(unwrap); }
function write(action, id, data) { return writeRPC(action, id || '', JSON.stringify(data || {})).then(unwrap); }
function notify(error) { ui.addNotification(null, E('p', {}, [error.message]), 'error'); }

function button(label, action, readonly, style) {
	var pending = false;
	function disabled() { return pending || (typeof readonly === 'function' ? readonly() : readonly); }
	var node = E('button', {
		'class': 'cbi-button ' + (style || 'cbi-button-action'), 'type': 'button',
		'disabled': disabled() || null,
		'click': function() {
			if (disabled()) return Promise.resolve();
			pending = true;
			node.disabled = true;
			return Promise.resolve().then(action).catch(notify).finally(function() { pending = false; node.updateDisabled(); });
		}
	}, [label]);
	node.updateDisabled = function() { node.disabled = !!disabled(); };
	return node;
}

function section(title, nodes, extraClass) {
	return E('section', { 'class': 'cbi-section ffvpn-panel ' + (extraClass || '') }, [E('h3', {}, [title])].concat(nodes));
}

function quotaText(usage) {
	if (!usage) return _('Unknown');
	if (usage.unlimited) return _('Unlimited');
	var limit = Number(usage.limit_bytes || 0), remaining = Number(usage.remaining_bytes || 0);
	return (remaining / 1e9).toFixed(2) + ' / ' + (limit / 1e9).toFixed(2) + ' GB (' +
		(limit > 0 ? remaining * 100 / limit : 0).toFixed(1) + '%)';
}

function clientURL(raw, lanIP) {
	var url = new URL(raw);
	if (/^(0\.0\.0\.0|\[::\]|127\..*|localhost|\[::1\])$/.test(url.hostname)) {
		var host = lanIP || window.location.hostname;
		url.hostname = host.indexOf(':') >= 0 && host[0] !== '[' ? '[' + host + ']' : host;
	}
	return url.href;
}

function statusTable(rows) {
	return E('table', { 'class': 'table ffvpn-status-table', 'aria-labelledby': 'ffvpn-status-heading' }, [E('tbody', {}, rows.map(function(row) {
		return E('tr', { 'class': 'tr' }, [
			E('th', { 'class': 'td', 'scope': 'row', 'width': '25%' }, [row[0]]),
			E('td', { 'class': 'td', 'colspan': row[2] ? null : '2' }, [row[1]])
		].concat(row[2] ? [E('td', { 'class': 'td cbi-section-actions' }, [row[2]])] : []));
	}))]);
}

function controls(nodes) {
	return E('div', { 'class': 'ffvpn-controls' }, nodes);
}

function colored(text, tone) {
	var colors = { good: 'var(--ffvpn-success, #16803a)', bad: 'var(--error-color-low, #c62828)', warning: 'var(--ffvpn-warning, #916000)' };
	return E('span', { 'class': 'ffvpn-' + (tone || 'neutral'), 'style': tone ? 'color:' + colors[tone] + ';font-weight:600' : '' }, [text]);
}

function quotaTone(usage) {
	if (!usage) return null;
	if (usage.unlimited) return 'good';
	var limit = Number(usage.limit_bytes), remaining = Number(usage.remaining_bytes);
	if (!(limit > 0) || !Number.isFinite(remaining)) return null;
	return remaining <= limit * 0.1 ? 'bad' : remaining <= limit * 0.25 ? 'warning' : 'good';
}

function countryLabel(code) {
	code = (code || '').trim().toUpperCase();
	if (!/^[A-Z]{2}$/.test(code) || code === 'ZZ' || code === 'XX') return '';
	return E('span', { 'class': 'ffvpn-country' }, [E('span', { 'class': 'ffvpn-flag', 'aria-hidden': 'true' }, [
		String.fromCodePoint(127397 + code.charCodeAt(0), 127397 + code.charCodeAt(1))
	]), ' ', code]);
}

function copyURL(value) {
	if (navigator.clipboard && window.isSecureContext)
		return navigator.clipboard.writeText(value);
	var text = E('input', { 'type': 'text', 'readonly': true, 'value': value, 'style': 'width:100%' });
	ui.showModal(_('Copy proxy URL'), [E('p', {}, [_('Copy this URL into your proxy client.')]), text,
		E('div', { 'class': 'right' }, [button(_('Close'), ui.hideModal, false)])]);
	text.select();
}

return view.extend({
	load: function() {
		return Promise.all([
			read('status').then(function(status) { return { status: status }; }).catch(function(err) { return { error: err.message }; }),
			lanStatus().catch(function() { return {}; })
		]).then(function(data) { data[0].lan = data[1]; return data[0]; });
	},

	render: function(data) {
		var self = this;
		this.readonly = !L.hasViewPermission();
		this.busy = false;
		this.accounts = null;
		this.lanIP = ((data.lan || {})['ipv4-address'] || [])[0];
		this.lanIP = this.lanIP ? this.lanIP.address : null;
		this.stateNode = E('div');
		this.versionNode = E('small', { 'class': 'ffvpn-version' });
		this.statusNode = E('div');
		this.trafficNode = E('p', { 'class': 'cbi-section-descr ffvpn-traffic-summary' }, [_('Loading account quotas...')]);
		this.accountsNode = E('div');
		this.linksNode = E('div');
		this.settingsNode = E('div', { 'class': 'ffvpn-settings' }, [E('p', {}, [_('Open this tab to load settings.')])]);
		this.logsNode = E('pre', { 'class': 'ffvpn-logs' });
		this.stopServiceButton = this.actionButton(_('Stop'), function() { return write('stop_service').then(self.refresh.bind(self)); }, function() { return !self.status; });
		this.restartServiceButton = this.actionButton(_('Restart'), function() { return write('restart_service').then(self.refresh.bind(self)); }, function() { return !self.service && !self.status; });
		this.enableButton = this.actionButton(_('Enable'), function() { return write('service_enable', '', { enabled: true }).then(self.refresh.bind(self)); }, function() { return !self.service || (self.service.enabled && !!self.status); }, 'cbi-button-positive');
		this.disableButton = this.actionButton(_('Disable'), function() { return write('service_enable', '', { enabled: false }).then(self.refresh.bind(self)); }, function() { return !self.service || (!self.service.enabled && !self.status); }, 'cbi-button-negative');
		this.restartServiceButton.title = _('Restart the whole backend without changing boot startup.');
		this.stopServiceButton.title = _('Stop the whole backend without changing boot startup.');
		this.enableButton.title = _('Start the service and enable boot startup.');
		this.disableButton.title = _('Stop the service and disable boot startup.');
		this.quotaButton = this.actionButton(_('Refresh quota'), this.refreshQuotas.bind(this), function() { return !self.status || !self.accounts || !self.accounts.length; });
		this.quotaButton.title = _('Refresh quota for all saved accounts.');
		this.renderStatus(data.status, data.error);
		poll.add(this.refresh.bind(this), 5);
		this.refresh().catch(notify);
		var overview = this.overview = E('div', { 'data-tab': 'overview', 'data-tab-title': _('Overview') }, [
			E('div', { 'class': 'ffvpn-page-header' }, [E('h2', {}, [_('Firefox VPN'), ' ', this.versionNode]),
				E('a', { 'class': 'cbi-button cbi-button-neutral', 'href': 'https://github.com/BercX/firefox-vpn-openwrt', 'target': '_blank', 'rel': 'noopener noreferrer', 'title': _('Source code on GitHub') }, ['GitHub'])]),
			E('section', { 'class': 'cbi-section', 'aria-labelledby': 'ffvpn-status-heading' }, [
				E('h3', { 'id': 'ffvpn-status-heading' }, [_('Status')]), this.statusNode
			]),
			E('section', { 'class': 'cbi-section', 'aria-labelledby': 'ffvpn-proxy-heading' }, [
				E('h3', { 'id': 'ffvpn-proxy-heading' }, [_('Proxy access')]), this.linksNode
			]),
			E('section', { 'class': 'cbi-section', 'aria-labelledby': 'ffvpn-accounts-heading' }, [
				E('h3', { 'id': 'ffvpn-accounts-heading' }, [_('Accounts')]), this.trafficNode, this.accountsNode,
				E('div', { 'class': 'cbi-section-create' }, [
					this.actionButton(_('Add account'), function() { self.signIn(); }, function() { return !self.status; }, 'cbi-button-add'), this.quotaButton
				])
			])
		]);
		var settings = E('div', { 'data-tab': 'settings', 'data-tab-title': _('Settings'), 'cbi-tab-active': function() {
			if (!self.settingsLoaded) self.settings().catch(notify);
		} }, [this.settingsNode]);
		var logs = E('div', { 'data-tab': 'logs', 'data-tab-title': _('Logs'), 'cbi-tab-active': function() {
			window.requestAnimationFrame(function() {
				// Measure with a viewport-tall viewer so footer and theme spacing count too.
				self.logsNode.style.setProperty('--ffvpn-logs-space', '0px');
				self.logsNode.style.setProperty('--ffvpn-logs-space', Math.max(0, document.documentElement.scrollHeight - window.innerHeight + 1) + 'px');
				self.logsNode.scrollTop = self.logsNode.scrollHeight;
			});
		} }, [section(_('Logs'), [this.logsNode])]);
		var tabs = E('div', {}, [overview, settings, logs]);
		var page = this.page = E('div', { 'class': 'ffvpn' }, [
			E('link', { 'rel': 'stylesheet', 'href': L.resource('view/firefox-vpn/overview.css') + '?v=23' }), tabs
		]);
		ui.tabs.initTabGroup(tabs.childNodes);
		this.updateButtons();
		return page;
	},

	actionButton: function(label, action, unavailable, style) {
		var self = this;
		return button(label, function() {
			self.busy = true;
			self.updateButtons();
			return Promise.resolve().then(action).finally(function() { self.busy = false; self.updateButtons(); });
		}, function() { return self.readonly || self.busy || unavailable(); }, style);
	},

	updateButtons: function() {
		[this.overview, this.accountActionsNode].forEach(function(root) {
			if (root) root.querySelectorAll('button').forEach(function(node) { if (node.updateDisabled) node.updateDisabled(); });
		});
	},

	renderStatus: function(status, error) {
		var startup = this.service ? colored(this.service.enabled ? _('Enabled') : _('Disabled'), this.service.enabled ? 'good' : 'bad') : _('Unknown');
		var proxyState = colored(_('Unavailable'), 'bad');
		var rows = [];
		this.status = status || null;
		if (status && status.version) this.versionNode.textContent = 'v' + status.version;
		if (!status) {
			dom.content(this.linksNode, E('p', {}, [_('Start the service to see proxy connection URLs.')]));
			if (error && (!this.service || this.service.enabled)) rows.push([_('Error'), error]);
		} else {
			var proxy = status.proxy || {}, exit = status.exit || {}, state = status.state || {};
			var test = status.last_test || {};
			var country = countryLabel(exit.country_code) || countryLabel(proxy.country_code);
			var location = proxy.country_name || status.selection_name || status.selection || _('Recommended Location');
			if ((proxy.country_code || '').toUpperCase() === 'REC') location = _('Recommended Location');
			if (country) location = E('span', {}, [location, ' (', country, ')']);
			var tone = state.phase === 'running' && !proxy.wake_error ? 'good' : /^(off|error)$/.test(state.phase) || proxy.wake_error ? 'bad' : 'warning';
			var addresses = [exit.ipv4, exit.ipv6].filter(Boolean);
			var ip = status.testing ? _('Checking through the proxy...') : test.exit_error || test.error ? colored(test.exit_error || test.error, 'bad') : state.phase === 'off' ? _('Restart the service to check the public IP') : _('Waiting for a connection check');
			var phaseLabel = { running: _('Running'), starting: _('Starting'), waiting: _('Waiting'), error: _('Error'), off: _('Disabled') };
			proxyState = colored(phaseLabel[state.phase] || _('Unknown'), tone);
			rows = rows.concat([
				[_('Location'), location],
				[_('Public IPv4'), exit.ipv4 ? E('code', {}, [exit.ipv4]) : addresses.length ? '—' : ip],
				[_('Public IPv6'), exit.ipv6 ? E('code', {}, [exit.ipv6]) : '—'],
				[_('Active account'), status.active_account ? status.active_account.email : _('None')]
			]);
			if (proxy.wake_error || state.phase === 'error') rows.push([_('Connection error'), colored(proxy.wake_error || state.message || _('Failed'), 'bad')]);
			this.renderLinks(proxy);
		}
		dom.content(this.stateNode, [
			E('div', {}, [_('Service'), ': ', colored(status ? _('Running') : this.service && !this.service.enabled ? _('Stopped') : _('Unavailable / failed to start'), status ? 'good' : 'bad')]),
			E('div', {}, [_('Boot startup'), ': ', startup]),
			E('div', {}, [_('Proxy'), ': ', proxyState])
		]);
		dom.content(this.statusNode, statusTable([
			[_('State'), this.stateNode, controls([this.restartServiceButton, this.stopServiceButton, this.enableButton, this.disableButton])]
		].concat(rows)));
		this.updateButtons();
	},

	renderLinks: function(proxy) {
		var self = this;
		if (!proxy.listen) {
			dom.content(this.linksNode, E('p', {}, [_('Start or restart the service to see proxy connection URLs.')]));
			return;
		}
		var loopback = /^(127\.|localhost:|\[::1\]:)/.test(proxy.listen);
		dom.content(this.linksNode, [E('table', { 'class': 'table cbi-section-table ffvpn-proxy-table', 'aria-labelledby': 'ffvpn-proxy-heading' }, [E('tbody', {}, ['socks5', 'http'].map(function(scheme) {
			var url = clientURL(scheme + '://' + proxy.listen, self.lanIP);
			return E('tr', { 'class': 'tr cbi-section-table-row' }, [
				E('td', { 'class': 'td', 'width': '25%' }, [E('label', { 'for': 'ffvpn-url-' + scheme }, [scheme.toUpperCase()])]),
				E('td', { 'class': 'td col-10' }, [E('input', { 'id': 'ffvpn-url-' + scheme, 'class': 'cbi-input-text', 'type': 'text', 'readonly': true, 'spellcheck': 'false', 'value': url, 'aria-label': scheme.toUpperCase() + ' ' + _('proxy URL') })]),
				E('td', { 'class': 'td cbi-section-actions' }, [controls([button(_('Copy URL'), function() {
					if (!proxy.login_required) return copyURL(url);
					return write('connection_links').then(function(links) { return copyURL(clientURL(links[scheme], self.lanIP)); });
				}, function() { return loopback || self.busy || !self.status || self.status.state.phase !== 'running' || (proxy.login_required && self.readonly); })])])
			]);
		}))]),
			loopback ? E('div', { 'class': 'alert-message warning ffvpn-warning', 'role': 'status' }, [
				E('strong', {}, [_('LAN access is unavailable: the listener is router-only.')]),
				E('p', {}, [_('Set Listen address to the router LAN IP in Settings. Keep WAN access blocked.')]),
				E('a', { 'href': '#', 'click': function(ev) {
					ev.preventDefault();
					self.page.querySelector('.cbi-tabmenu [data-tab="settings"] a').click();
				} }, [_('Open Settings')])
			]) : proxy.login_required ? E('p', { 'class': 'ffvpn-note' }, [_('Copy includes the saved proxy username and password. Treat the copied URL as a secret.')]) : ''
		]);
	},

	probe: function() {
		var self = this;
		if (!this.status || this.status.state.phase !== 'running') return Promise.resolve();
		return write('test').then(function() { return self.refresh(); });
	},

	refresh: function() {
		var self = this;
		return Promise.all([
			read('service').then(function(service) { self.service = service; }).catch(function() { self.service = null; }),
			read('status').then(function(status) { return { status: status }; }).catch(function(err) { return { error: err.message }; }),
			lanStatus().then(function(data) { self.lanIP = ((data['ipv4-address'] || [])[0] || {}).address || null; }).catch(function() { self.lanIP = null; })
		]).then(function(data) {
			var status = data[1].status;
			self.renderStatus(status, data[1].error);
			if (!status) {
				dom.content(self.accountsNode, E('p', {}, [_('Start the service to manage accounts.')]));
				dom.content(self.trafficNode, [_('Available traffic is unknown while the service is stopped.')]);
				return;
			}
			var key = status.proxy && status.proxy.started_at + ':' + (status.active_account || {}).id;
			if (!self.readonly && status.state.phase === 'running' && key && key !== self.probeKey) {
				self.probeKey = key;
				if (!status.testing) write('test').catch(notify);
			}
			return Promise.all([
				read('accounts').then(function(data) { self.renderAccounts(data.accounts || []); }).catch(function(err) {
					dom.content(self.accountsNode, E('p', {}, [err.message]));
					dom.content(self.trafficNode, [_('Available traffic is unknown.')]);
				}),
				read('logs').then(function(data) {
					var top = self.logsNode.scrollTop;
					var follow = self.logsNode.scrollHeight - self.logsNode.clientHeight - top <= 1;
					self.logsNode.textContent = (data.entries || []).slice(-100).map(function(e) {
						return [e.time, e.level, e.message].join(' ');
					}).join('\n') || _('No log entries yet.');
					self.logsNode.scrollTop = follow ? self.logsNode.scrollHeight : top;
				}).catch(function(err) { self.logsNode.textContent = err.message; })
			]);
		}).catch(function(err) { self.renderStatus(null, err.message); });
	},

	renderAccounts: function(accounts) {
		var self = this;
		this.accounts = accounts;
		var remaining = 0, limit = 0, known = 0, unknown = 0, unlimited = 0, enabled = 0;
		accounts.filter(function(acc) { return acc.enabled; }).forEach(function(acc) {
			enabled++;
			var usage = acc.usage;
			if (usage && usage.unlimited) { unlimited++; return; }
			if (!usage || !Number.isFinite(usage.limit_bytes) || usage.limit_bytes <= 0 || !Number.isFinite(usage.remaining_bytes) || usage.remaining_bytes < 0) { unknown++; return; }
			known++;
			limit += usage.limit_bytes;
			remaining += Math.min(usage.remaining_bytes, usage.limit_bytes);
		});
		var coverage = [enabled + ' ' + (enabled === 1 ? _('enabled account') : _('enabled accounts'))];
		if (unlimited) coverage.push(unlimited + ' ' + _('unlimited'));
		if (unknown) coverage.push(unknown + ' ' + _('unknown quota'));
		dom.content(this.trafficNode, [E('small', {}, [
			colored(unlimited ? _('Unlimited') : known || !enabled ? (remaining / 1e9).toFixed(2) + ' GB ' + _('remaining') : _('Unknown'), unlimited ? 'good' : known ? quotaTone({ limit_bytes: limit, remaining_bytes: remaining }) : null),
			' · ', coverage.join(', ')
		].concat(unlimited && known ? [' · ' + (remaining / 1e9).toFixed(2) + ' GB ' + _('metered traffic remaining')] : []))]);
		if (!accounts.length) {
			dom.content(this.accountsNode, E('p', {}, [_('No accounts. Add a Firefox account to get started.')]));
			this.updateButtons();
			return;
		}
		var labels = [_('Account'), _('Status'), _('Quota remaining'), _('Actions')];
		var statuses = { ready: _('Ready'), reserved: _('Reserved'), unavailable: _('Unavailable'), reauth_required: _('Sign in required') };
		dom.content(this.accountsNode, E('div', { 'class': 'ffvpn-accounts' }, [E('table', { 'class': 'table cbi-section-table', 'aria-labelledby': 'ffvpn-accounts-heading' }, [
			E('thead', {}, [E('tr', { 'class': 'tr cbi-section-table-titles' }, labels.map(function(s) { return E('th', { 'class': 'th', 'scope': 'col' }, [s]); }))]),
			E('tbody', {}, accounts.map(function(acc) {
				function action(name, data) { return function() { return write(name, acc.id, data).then(self.refresh.bind(self)); }; }
				function unavailable() { return !self.status; }
				var tone = quotaTone(acc.usage);
				return E('tr', { 'class': 'tr cbi-section-table-row' }, [
					E('td', { 'class': 'td', 'data-title': labels[0] }, [acc.email]),
					E('td', { 'class': 'td', 'data-title': labels[1] }, [acc.enabled ? (acc.active ? E('strong', {}, [_('Active')]) : statuses[acc.status] || acc.status || _('Unknown')) : _('Disabled'), acc.reason ? E('div', { 'class': 'ffvpn-note' }, [acc.reason]) : '']),
					E('td', { 'class': 'td', 'data-title': labels[2] }, [colored(quotaText(acc.usage), tone)]),
					E('td', { 'class': 'td cbi-section-actions' }, [controls([
						self.actionButton(_('Use now'), action('account_activate'), function() { return !self.status || acc.active || !acc.enabled; }), ' ',
						self.actionButton(acc.enabled ? _('Disable') : _('Enable'), action('account_enable', { enabled: !acc.enabled }), unavailable),
						self.actionButton(_('Remove'), function() { self.removeAccount(acc); }, unavailable, 'cbi-button-negative')
					])])
				]);
			}))
		])]));
		this.updateButtons();
	},

	refreshQuotas: async function() {
		var accounts = (this.accounts || []).slice(), errors = [];
		for (var i = 0; i < accounts.length; i++) {
			try { await write('account_refresh', accounts[i].id); }
			catch (err) { errors.push(accounts[i].email + ': ' + err.message); }
		}
		await this.refresh();
		await this.probe();
		if (errors.length) throw new Error(errors.join('\n'));
	},

	removeAccount: function(acc) {
		var self = this;
		function unavailable() { return !self.status || !self.accounts || !self.accounts.some(function(a) { return a.id === acc.id; }); }
		function close() { self.accountActionsNode = null; ui.hideModal(); }
		this.accountActionsNode = E('div', { 'class': 'ffvpn' }, [E('p', {}, [_('Remove saved credentials for %s?').format(acc.email)]), controls([
			button(_('Cancel'), close, false),
			this.actionButton(_('Remove'), function() { return write('account_remove', acc.id).then(close).then(self.refresh.bind(self)); }, unavailable, 'cbi-button-negative')
		])]);
		ui.showModal(_('Remove account'), [this.accountActionsNode]);
	},

	signIn: function(email) {
		var self = this;
		var emailInput = E('input', { 'id': 'ffvpn-email', 'type': 'email', 'value': email || '', 'autocomplete': 'username', 'required': true });
		var password = E('input', { 'id': 'ffvpn-password', 'type': 'password', 'autocomplete': 'current-password', 'required': true });
		ui.showModal(_('Sign in to Firefox'), [
			E('p', {}, [_('Use HTTPS for LuCI when entering credentials. The core stores tokens and an encrypted password on the router for renewal; keep router backups private.')]),
			E('p', {}, [E('label', { 'for': 'ffvpn-email' }, [_('Email')]), E('br'), emailInput]),
			E('p', {}, [E('label', { 'for': 'ffvpn-password' }, [_('Password')]), E('br'), password]),
			E('div', { 'class': 'right' }, [
				button(_('Cancel'), ui.hideModal, false), ' ',
				button(_('Sign in'), function() {
					if (!emailInput.reportValidity() || !password.reportValidity()) return;
					var request = write('signin_start', '', { email: emailInput.value, password: password.value });
					password.value = '';
					return request.then(function(state) { self.followSignIn(state); });
				}, self.readonly)
			])
		]);
	},

	followSignIn: function(initial) {
		var self = this, state = initial, last = '', closed = false;
		var content = E('div');
		var answer = E('input', { 'id': 'ffvpn-code', 'type': 'text', 'autocomplete': 'one-time-code', 'maxlength': 1024 });
		var instruction = E('p'), problem = E('p', { 'class': 'alert-message warning' });
		var image = E('img', { 'alt': _('Firefox Accounts verification image'), 'style': 'max-width:100%' });
		var answerRow = E('p', {}, [E('label', { 'for': 'ffvpn-code' }, [_('Verification code or image answer')]), E('br'), answer]);
		function close() { closed = true; poll.remove(tick); ui.hideModal(); return self.refresh(); }
		var submit = button(_('Continue'), function() {
			if (/^(done|failed|cancelled)$/.test(state.step)) return close();
			if (!/^need_/.test(state.step)) return;
			return write('signin_answer', state.id, { value: answer.value }).then(function(next) { answer.value = ''; update(next); });
		}, self.readonly);
		var cancel = button(_('Cancel'), function() { return write('signin_cancel', state.id).then(close); }, self.readonly);
		var dismiss = button(_('Close'), close, false);
		function update(next) {
			if (closed) return;
			state = next;
			instruction.textContent = state.message;
			problem.textContent = state.error || '';
			problem.style.display = state.error ? '' : 'none';
			var needsInput = /^need_/.test(state.step), done = /^(done|failed|cancelled)$/.test(state.step);
			answerRow.style.display = needsInput && state.step !== 'need_email_link' ? '' : 'none';
			submit.style.display = needsInput ? '' : 'none';
			submit.disabled = self.readonly || !needsInput;
			cancel.style.display = done ? 'none' : '';
			dismiss.style.display = done ? '' : 'none';
			image.style.display = state.step === 'need_captcha' ? '' : 'none';
			if (state.step === 'need_captcha' && /^data:image\/(png|jpeg|gif);base64,/.test(state.captcha || '')) image.src = state.captcha;
			else image.removeAttribute('src');
			if (last !== state.step && needsInput && state.step !== 'need_email_link') answer.focus();
			last = state.step;
			if (state.step === 'done') return close();
			if (done) { poll.remove(tick); self.refresh(); }
		}
		function tick() {
			return read('signin', state.id).then(update).catch(function(err) {
				update({ id: state.id, step: 'failed', message: _('Sign-in interrupted. Start again.'), error: err.message });
			});
		}
		dom.content(content, [instruction, problem, image, answerRow, E('div', { 'class': 'right' }, [submit, ' ', cancel, ' ', dismiss])]);
		ui.showModal(_('Firefox account verification'), [content]);
		poll.add(tick, 2);
		update(initial);
	},

	settings: function() {
		var self = this;
		this.settingsLoaded = true;
		dom.content(this.settingsNode, E('p', {}, [_('Loading settings...')]));
		return Promise.all([read('settings'), read('locations').catch(function() { return { locations: [] }; })]).then(function(data) {
			var settings = data[0];
			settings.udp_mode = settings.udp_mode || 'dns';
			settings.log_level = settings.log_level || 'info';
			settings.dns_fallback = settings.dns_fallback ? '1' : '0';
			var fields = {
				access: ['listen', 'username', 'password'],
				connection: ['country', 'transport', 'udp_mode', 'max_connections'],
				dns: ['dns_servers', 'dns_fallback'],
				logging: ['log_level']
			};
			var values = {};
			Object.keys(fields).forEach(function(group) {
				values[group] = {};
				fields[group].forEach(function(key) { values[group][key] = settings[key]; });
			});
			values.connection.country = settings.country || 'recommended';
			var m = new form.JSONMap(values, _('Settings'));
			m.readonly = self.readonly;
			var s = m.section(form.NamedSection, 'access', 'access', _('Proxy access')), o;
			o = s.option(form.Value, 'listen', _('Listen address'), _('Default 0.0.0.0:1088 accepts LAN clients. Use your router LAN IP:1088 to bind one address, or 127.0.0.1:1088 for router-only access. Keep the port blocked on WAN.'));
			o.rmempty = false;
			o = s.option(form.Value, 'username', _('Proxy username'), _('Set a username and password to require proxy authentication. Clear the username to disable authentication.'));
			o = s.option(form.Value, 'password', _('Proxy Password'), _('Leave empty to keep the existing password.'));
			o.password = true;
			s = m.section(form.NamedSection, 'connection', 'connection', _('Connection'));
			o = s.option(form.ListValue, 'country', _('Location'), _('Mozilla may restrict access to locked locations.'));
			o.default = 'recommended';
			o.rmempty = false;
			o.value('recommended', _('Recommended'));
			o.value('fastest', _('Fastest'));
			(data[1].locations || []).forEach(function(loc) { if (!loc.recommended) o.value(loc.code, E('span', {}, [countryLabel(loc.code), ': ' + loc.name + (loc.locked ? ' (' + _('locked') + ')' : '')])); });
			o = s.option(form.ListValue, 'transport', _('Transport'));
			o.value('h2', _('HTTP/2 (TCP)'));
			o.value('h3', _('HTTP/3 (QUIC, with HTTP/2 fallback)'));
			o = s.option(form.ListValue, 'udp_mode', _('SOCKS5 UDP'), _('DNS is sent to Cloudflare over HTTPS through the proxy, regardless of the requested DNS server. Other UDP traffic is blocked, never sent directly.'));
			o.value('off', _('Disabled'));
			o.value('dns', _('DNS only (recommended)'));
			o = s.option(form.Value, 'max_connections', _('Maximum connections'));
			o.datatype = 'range(1,4096)'; o.rmempty = false;
			s = m.section(form.NamedSection, 'dns', 'dns', _('DNS'));
			o = s.option(form.DynamicList, 'dns_servers', _('DNS servers'), _('HTTPS URL, tls://host, or IP[:port]. An empty list uses system DNS. These resolve service endpoints for the core; SOCKS5 hostname targets are resolved by the upstream proxy.'));
			o = s.option(form.Flag, 'dns_fallback', _('Fall back to system DNS'));
			s = m.section(form.NamedSection, 'logging', 'logging', _('Logging'));
			o = s.option(form.ListValue, 'log_level', _('Log level'), _('Minimum level for system and LuCI logs. Applies immediately. Debug may reveal destination hosts and other sensitive connection details.'));
			o.value('off', _('Disable'));
			o.value('error', _('Error'));
			o.value('warn', _('Warning'));
			o.value('info', _('Info (default)'));
			o.value('debug', _('Debug'));
			o.rmempty = false;
			return m.render().then(function(node) {
				dom.content(self.settingsNode, [node, E('div', { 'class': 'right' }, [
					button(_('Save and apply'), function() {
						return m.save(function() {
							var v = {};
							Object.keys(fields).forEach(function(group) {
								fields[group].forEach(function(key) { v[key] = m.data.get('json', group, key) || ''; });
							});
							v.max_connections = Number(v.max_connections);
							v.dns_fallback = v.dns_fallback === '1';
							v.dns_servers = v.dns_servers || [];
							v.allow_locked = !!settings.allow_locked;
							if (v.country === 'recommended') v.country = '';
							v.server = v.country === (settings.country || '') ? settings.server || '' : '';
							return write('settings', '', v);
						}).then(function() { return self.settings().then(self.refresh.bind(self)); });
					}, self.readonly, 'cbi-button-positive')
				])]);
			});
		}).catch(function(err) {
			self.settingsLoaded = false;
			dom.content(self.settingsNode, [E('p', {}, [err.message]), button(_('Retry'), self.settings.bind(self), false)]);
		});
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
