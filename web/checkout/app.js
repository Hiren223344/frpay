(function () {
  'use strict';

  // ---- config -------------------------------------------------------
  const params = new URLSearchParams(window.location.search);
  const FRENIXPAY_API_BASE = params.get('api') || window.FRENIXPAY_API_BASE || 'http://localhost:8080';
  const CREATE_ORDER_URL = '/checkout/create-order';
  const POLL_INTERVAL_MS = 4000;

  const AMOUNT_USD = params.get('amount') || '249.00';
  const MERCHANT_NAME = params.get('merchant') || 'Acme Studio';
  const ITEM_LABEL = params.get('item') || 'Pro plan · annual';

  // ---- tiny dom helpers ----------------------------------------------
  const el = (id) => document.getElementById(id);
  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));
  }
  function showStep(id) {
    document.querySelectorAll('.step').forEach((s) => { s.hidden = s.id !== id; });
  }
  function fmtUSD(v) {
    const n = Number(v);
    return isNaN(n) ? v : '$' + n.toFixed(2);
  }
  function pad2(n) { return String(n).padStart(2, '0'); }

  // ---- state -----------------------------------------------------------
  const state = {
    chains: [],          // from GET /v1/chains
    selectedChain: null,
    order: null,          // last-known order response
    pollTimer: null,
    countdownTimer: null,
    copyResetTimer: null,
  };

  // ---- sidebar -----------------------------------------------------
  function renderSidebar() {
    el('merchantName').textContent = MERCHANT_NAME;
    el('brandMark').textContent = MERCHANT_NAME.trim().charAt(0).toUpperCase() || '?';
    el('amountUSD').textContent = fmtUSD(AMOUNT_USD);
    el('lineItems').innerHTML =
      `<div class="row"><span>${escapeHtml(ITEM_LABEL)}</span><span>${fmtUSD(AMOUNT_USD)}</span></div>`;
  }

  function renderAmountCrypto() {
    if (state.order) {
      el('amountCrypto').textContent = `≈ ${state.order.amount_token} ${state.order.token}`;
    } else {
      el('amountCrypto').textContent = '';
    }
  }

  // ---- steps indicator -----------------------------------------------
  const STEP_ORDER = ['select', 'send', 'confirming'];
  function renderSteps(currentKey) {
    const labels = ['Network', 'Pay', 'Confirm'];
    const cur = currentKey === 'confirmed' ? 3 : STEP_ORDER.indexOf(currentKey);
    el('steps').innerHTML = labels.map((label, i) => {
      const done = i < cur, active = i === cur;
      const color = i <= cur ? '#1b1a17' : '#a19d94';
      const dotBg = done ? '#1f5b45' : active ? '#1b1a17' : '#efece6';
      const dotFg = i <= cur ? '#fff' : '#a19d94';
      const sep = i < 2 ? 'block' : 'none';
      return `<div class="step-item" style="color:${color}">
        <span class="dot" style="background:${dotBg};color:${dotFg}">${done ? '✓' : i + 1}</span>
        ${label}
        <span class="sep" style="display:${sep}"></span>
      </div>`;
    }).join('');
  }

  // ---- countdown -------------------------------------------------------
  function startCountdown(expiresAtISO) {
    clearInterval(state.countdownTimer);
    const expiresAt = new Date(expiresAtISO).getTime();
    const createdSpan = Math.max(1, (expiresAt - Date.now()) / 1000);
    const totalSeconds = createdSpan;

    function tick() {
      const remainingMs = expiresAt - Date.now();
      const remaining = Math.max(0, Math.round(remainingMs / 1000));
      el('timerBox').hidden = false;
      el('timerLabel').textContent = pad2(Math.floor(remaining / 60)) + ':' + pad2(remaining % 60);
      const pct = Math.max(0, Math.min(100, (remaining / totalSeconds) * 100));
      el('timerFill').style.width = pct + '%';
      if (remaining <= 0) {
        clearInterval(state.countdownTimer);
        if (state.order && (state.order.status === 'pending' || state.order.status === 'confirming')) {
          onOrderUpdate(Object.assign({}, state.order, { status: 'expired' }));
        }
      }
    }
    tick();
    state.countdownTimer = setInterval(tick, 1000);
  }

  // ---- QR ---------------------------------------------------------------
  function renderQR(text) {
    const holder = el('qrHolder');
    holder.innerHTML = '';
    try {
      const qr = qrcode(6, 'M');
      qr.addData(text);
      qr.make();
      holder.innerHTML = qr.createSvgTag(4, 8);
    } catch (e) {
      holder.innerHTML = '<span class="muted small">QR unavailable</span>';
      console.error('QR generation failed', e);
    }
  }

  // ---- copy buttons ----------------------------------------------------
  function wireCopyButton(btn, getText) {
    btn.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(getText());
        btn.textContent = 'Copied';
        btn.classList.add('copied');
        clearTimeout(state.copyResetTimer);
        state.copyResetTimer = setTimeout(() => {
          btn.textContent = 'Copy';
          btn.classList.remove('copied');
        }, 1400);
      } catch (e) {
        console.error('clipboard write failed', e);
      }
    });
  }

  // ---- network selection step ------------------------------------------
  async function loadChains() {
    const res = await fetch(FRENIXPAY_API_BASE + '/v1/chains');
    if (!res.ok) throw new Error('failed to load available networks');
    state.chains = await res.json();
  }

  function renderNetworkList() {
    const list = el('networkList');
    if (!state.chains.length) {
      list.innerHTML = '<p class="muted small">No networks are currently available. Please try again shortly.</p>';
      el('continueBtn').disabled = true;
      return;
    }
    if (!state.selectedChain) state.selectedChain = state.chains[0].chain;

    list.innerHTML = '';
    state.chains.forEach((c) => {
      const selected = c.chain === state.selectedChain;
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'network-item' + (selected ? ' selected' : '');
      btn.innerHTML = `
        <span class="name">${escapeHtml(c.label)}</span>
        <span class="confs">${c.required_confirmations} confirmations</span>
        <span class="radio"></span>`;
      btn.addEventListener('click', () => {
        state.selectedChain = c.chain;
        renderNetworkList();
      });
      list.appendChild(btn);
    });
    el('continueBtn').disabled = false;
  }

  // ---- order creation ----------------------------------------------------
  async function createOrder() {
    showStep('stepCreating');
    renderSteps('select');
    try {
      const res = await fetch(CREATE_ORDER_URL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ amount_usd: AMOUNT_USD, chain: state.selectedChain }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error || `order creation failed (${res.status})`);
      onOrderUpdate(body);
      const url = new URL(window.location.href);
      url.searchParams.set('order_id', body.order_id);
      window.history.replaceState({}, '', url);
      startPolling(body.order_id);
    } catch (e) {
      showError(e.message || 'Could not create order.');
    }
  }

  // ---- polling ------------------------------------------------------------
  function startPolling(orderId) {
    clearInterval(state.pollTimer);
    const poll = async () => {
      try {
        const res = await fetch(`${FRENIXPAY_API_BASE}/v1/orders/${orderId}`);
        if (res.status === 404) {
          clearInterval(state.pollTimer);
          showError('This order could not be found. It may have been from a previous session.');
          return;
        }
        if (!res.ok) return; // transient error; try again next tick
        const order = await res.json();
        onOrderUpdate(order);
      } catch (e) {
        // network hiccup; keep polling silently
        console.warn('poll failed', e);
      }
    };
    poll();
    state.pollTimer = setInterval(poll, POLL_INTERVAL_MS);
  }

  function stopTimers() {
    clearInterval(state.pollTimer);
    clearInterval(state.countdownTimer);
  }

  function chainLabel(chainKey) {
    const found = state.chains.find((c) => c.chain === chainKey);
    return found ? found.label : chainKey;
  }

  // ---- render order state ------------------------------------------------
  function onOrderUpdate(order) {
    state.order = order;
    renderAmountCrypto();
    el('orderMeta').textContent = `${order.order_id.slice(0, 8)}… · ${chainLabel(order.chain)}`;

    switch (order.status) {
      case 'pending':
      case 'confirming': {
        const isConfirming = order.status === 'confirming';
        renderSteps(isConfirming ? 'confirming' : 'send');
        startCountdown(order.expires_at);

        if (isConfirming) {
          showStep('stepConfirming');
          const pct = order.required_confirmations > 0
            ? Math.min(100, (order.confirmations / order.required_confirmations) * 100)
            : 0;
          el('confProgress').style.width = pct + '%';
          el('confLabel').textContent = `${order.confirmations} / ${order.required_confirmations} confirmations`;
          el('confTxHash').textContent = order.tx_hash ? `tx ${order.tx_hash}` : '';
        } else {
          el('sendNetwork').textContent = chainLabel(order.chain);
          el('sendNetwork2').textContent = chainLabel(order.chain);
          el('sendAmount').textContent = `${order.amount_token} ${order.token}`;
          el('sendAddress').textContent = order.deposit_address;
          renderQR(order.qr_string || order.deposit_address);
          showStep('stepSend');
        }
        break;
      }
      case 'confirmed': {
        stopTimers();
        renderSteps('confirmed');
        el('timerBox').hidden = true;
        el('confirmedText').textContent =
          `${MERCHANT_NAME} received ${order.amount_token} ${order.token}.`;
        el('receiptNetwork').textContent = chainLabel(order.chain);
        el('receiptTx').textContent = order.tx_hash
          ? order.tx_hash.slice(0, 10) + '…' + order.tx_hash.slice(-6)
          : '—';
        showStep('stepConfirmed');
        break;
      }
      case 'expired': {
        stopTimers();
        el('timerBox').hidden = true;
        showStep('stepExpired');
        break;
      }
      default: {
        showError(`Unexpected order status: ${order.status}`);
      }
    }
  }

  function showError(message) {
    stopTimers();
    el('errorText').textContent = message;
    showStep('stepError');
  }

  // ---- reset / restart --------------------------------------------------
  function resetToSelect() {
    stopTimers();
    state.order = null;
    state.selectedChain = null;
    const url = new URL(window.location.href);
    url.searchParams.delete('order_id');
    window.history.replaceState({}, '', url);
    renderAmountCrypto();
    el('timerBox').hidden = true;
    renderSteps('select');
    renderNetworkList();
    showStep('stepSelect');
  }

  // ---- init --------------------------------------------------------------
  async function init() {
    renderSidebar();
    el('continueBtn').addEventListener('click', createOrder);
    el('resetBtn').addEventListener('click', resetToSelect);
    el('restartBtn').addEventListener('click', resetToSelect);
    el('retryBtn').addEventListener('click', resetToSelect);
    el('returnBtn').addEventListener('click', () => {
      const ret = params.get('return_url');
      if (ret) window.location.href = ret;
    });
    wireCopyButton(el('copyAmountBtn'), () => (state.order ? state.order.amount_token : ''));
    wireCopyButton(el('copyAddressBtn'), () => (state.order ? state.order.deposit_address : ''));

    try {
      await loadChains();
    } catch (e) {
      showError('Could not reach the payment service. Please try again shortly.');
      return;
    }

    const existingOrderId = params.get('order_id');
    if (existingOrderId) {
      renderSteps('send');
      showStep('stepCreating');
      startPolling(existingOrderId);
      return;
    }

    renderSteps('select');
    renderNetworkList();
    showStep('stepSelect');
  }

  document.addEventListener('DOMContentLoaded', init);
})();
