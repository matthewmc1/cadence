import '../styles/tokenui.css';
import { useEffect, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { api, apiBase, ApiError } from '../api/client';
import type { ApiTokenInfo } from '../api/types';

function mcpConfig(token: string): string {
  return JSON.stringify(
    {
      mcpServers: {
        cadence: {
          command: 'node',
          args: ['/path/to/cadence/mcp/dist/index.js'],
          env: { CADENCE_API_TOKEN: token, CADENCE_API_URL: apiBase },
        },
      },
    },
    null,
    2,
  );
}

export function TokensModal() {
  const { tokensOpen } = useApp();
  const { closeTokens } = useActions();

  const [tokens, setTokens] = useState<ApiTokenInfo[]>([]);
  const [name, setName] = useState('MCP server');
  const [busy, setBusy] = useState(false);
  const [fresh, setFresh] = useState<string | null>(null); // the just-created plaintext token
  const [copied, setCopied] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = async () => {
    try {
      const { tokens } = await api.listTokens();
      setTokens(tokens);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not load tokens');
    }
  };

  useEffect(() => {
    if (tokensOpen) {
      setFresh(null);
      setError(null);
      void load();
    }
  }, [tokensOpen]);

  if (!tokensOpen) return null;

  const create = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.createToken(name.trim() || 'MCP server');
      setFresh(res.token);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not create token');
    }
    setBusy(false);
  };

  const revoke = async (id: string) => {
    try {
      await api.revokeToken(id);
      setTokens((t) => t.filter((x) => x.id !== id));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not revoke');
    }
  };

  const copy = (text: string, key: string) => {
    void navigator.clipboard?.writeText(text);
    setCopied(key);
    window.setTimeout(() => setCopied((c) => (c === key ? null : c)), 1500);
  };

  return (
    <div className="tk-scrim" onMouseDown={closeTokens}>
      <aside className="tk" role="dialog" aria-modal="true" aria-label="API tokens" onMouseDown={(e) => e.stopPropagation()}>
        <header className="tk__head">
          <div>
            <span className="eyebrow">Connect</span>
            <h2 className="tk__title serif">API tokens</h2>
          </div>
          <button className="tk__close" onClick={closeTokens} aria-label="Close">
            ✕
          </button>
        </header>

        <div className="tk__body">
          <p className="tk__intro">
            Generate a personal access token to connect the{' '}
            <strong>Cadence MCP server</strong> (or any API client). It's scoped to your workspace and shown only once.
          </p>

          {error && <div className="tk__error">{error}</div>}

          {fresh ? (
            <div className="tk__fresh">
              <div className="tk__fresh-label">Your new token — copy it now, it won't be shown again:</div>
              <div className="tk__tokenrow">
                <code className="tk__token">{fresh}</code>
                <button className="tk__copy" onClick={() => copy(fresh, 'tok')}>
                  {copied === 'tok' ? 'Copied' : 'Copy'}
                </button>
              </div>
              <div className="tk__cfg-label">MCP config (Claude Desktop / Code):</div>
              <pre className="tk__cfg">{mcpConfig(fresh)}</pre>
              <button className="tk__copy tk__copy--block" onClick={() => copy(mcpConfig(fresh), 'cfg')}>
                {copied === 'cfg' ? 'Copied config' : 'Copy config'}
              </button>
              <button className="tk__new" onClick={() => setFresh(null)}>
                Done
              </button>
            </div>
          ) : (
            <div className="tk__create">
              <input
                className="tk__name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Token name (e.g. MCP server)"
                aria-label="Token name"
              />
              <button className="btn-primary tk__createbtn" onClick={create} disabled={busy}>
                {busy ? 'Creating…' : 'Create token'}
              </button>
            </div>
          )}

          <div className="tk__list-label">Active tokens</div>
          {tokens.length === 0 ? (
            <div className="tk__empty">No tokens yet.</div>
          ) : (
            <ul className="tk__list">
              {tokens.map((t) => (
                <li key={t.id} className="tk__item">
                  <div className="tk__item-main">
                    <span className="tk__item-name">{t.name}</span>
                    <span className="tk__item-meta">
                      created {new Date(t.createdAt).toLocaleDateString()} ·{' '}
                      {t.lastUsedAt ? `last used ${new Date(t.lastUsedAt).toLocaleDateString()}` : 'never used'}
                    </span>
                  </div>
                  <button className="tk__revoke" onClick={() => revoke(t.id)}>
                    Revoke
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>
    </div>
  );
}
