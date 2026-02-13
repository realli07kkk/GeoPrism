import { useState, useEffect } from 'react';
import './App.css';

// 动态导入 Wails 方法
declare global {
  interface Window {
    go: {
      main: {
        App: {
          ListProviders: () => Promise<any[]>;
          UpsertProvider: (provider: any) => Promise<void>;
          DeleteProvider: (id: string) => Promise<void>;
          TestProvider: (id: string) => Promise<any>;
          QueryDomain: (req: any) => Promise<any>;
          Greet: (name: string) => Promise<string>;
        };
      };
    };
  }
}

type Tab = 'query' | 'providers';

function App() {
  const [activeTab, setActiveTab] = useState<Tab>('query');
  const [providers, setProviders] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [testingId, setTestingId] = useState<string | null>(null);

  // 查询状态
  const [domain, setDomain] = useState('google.com');
  const [recordType, setRecordType] = useState('A');
  const [queryResults, setQueryResults] = useState<any>(null);
  const [queryError, setQueryError] = useState('');

  // Provider 编辑状态
  const [editingProvider, setEditingProvider] = useState<any>(null);
  const [showModal, setShowModal] = useState(false);

  // 加载 Provider 列表
  const loadProviders = async () => {
    try {
      const list = await window.go.main.App.ListProviders();
      setProviders(list);
    } catch (e) {
      console.error('Failed to load providers:', e);
    }
  };

  useEffect(() => {
    loadProviders();
  }, []);

  // 查询域名
  const handleQuery = async () => {
    if (!domain) return;
    setLoading(true);
    setQueryError('');
    setQueryResults(null);

    try {
      const result = await window.go.main.App.QueryDomain({
        domain,
        record_type: recordType,
        provider_ids: [],
        timeout: 5000,
        retry: 0,
      });
      setQueryResults(result);
    } catch (e: any) {
      setQueryError(e.message || 'Query failed');
    } finally {
      setLoading(false);
    }
  };

  // 测试 Provider
  const handleTest = async (id: string) => {
    setTestingId(id);
    try {
      const result = await window.go.main.App.TestProvider(id);
      alert(`Provider: ${result.provider_id}\nSuccess: ${result.success}\nMessage: ${result.message}\nLatency: ${result.latency_ms}ms`);
    } catch (e: any) {
      alert(`Test failed: ${e.message}`);
    } finally {
      setTestingId(null);
    }
  };

  // 删除 Provider
  const handleDelete = async (id: string) => {
    if (!confirm('Are you sure you want to delete this provider?')) return;
    try {
      await window.go.main.App.DeleteProvider(id);
      await loadProviders();
    } catch (e: any) {
      alert(`Delete failed: ${e.message}`);
    }
  };

  // 保存 Provider
  const handleSave = async () => {
    if (!editingProvider) return;
    try {
      await window.go.main.App.UpsertProvider(editingProvider);
      setShowModal(false);
      setEditingProvider(null);
      await loadProviders();
    } catch (e: any) {
      alert(`Save failed: ${e.message}`);
    }
  };

  // 新建 Provider
  const handleNew = () => {
    setEditingProvider({
      id: '',
      name: '',
      protocol: 'doh',
      endpoint: '',
      server_name: '',
      port: 853,
      timeout: 5000,
      enabled: true,
      tags: [],
      created_at: '',
      updated_at: '',
    });
    setShowModal(true);
  };

  // 编辑 Provider
  const handleEdit = (p: any) => {
    setEditingProvider({ ...p });
    setShowModal(true);
  };

  return (
    <div id="app">
      <header className="header">
        <h1>GeoPrism</h1>
        <nav className="tabs">
          <button
            className={activeTab === 'query' ? 'active' : ''}
            onClick={() => setActiveTab('query')}
          >
            DNS Query
          </button>
          <button
            className={activeTab === 'providers' ? 'active' : ''}
            onClick={() => setActiveTab('providers')}
          >
            Providers
          </button>
        </nav>
      </header>

      <main className="content">
        {activeTab === 'query' && (
          <div className="query-panel">
            <div className="query-form">
              <div className="form-group">
                <label>Domain</label>
                <input
                  type="text"
                  value={domain}
                  onChange={(e) => setDomain(e.target.value)}
                  placeholder="e.g., google.com"
                />
              </div>
              <div className="form-group">
                <label>Record Type</label>
                <select value={recordType} onChange={(e) => setRecordType(e.target.value)}>
                  <option value="A">A</option>
                  <option value="AAAA">AAAA</option>
                  <option value="CNAME">CNAME</option>
                  <option value="TXT">TXT</option>
                  <option value="NS">NS</option>
                  <option value="MX">MX</option>
                </select>
              </div>
              <button className="btn-primary" onClick={handleQuery} disabled={loading}>
                {loading ? 'Querying...' : 'Query'}
              </button>
            </div>

            {queryError && <div className="error">{queryError}</div>}

            {queryResults && (
              <div className="results">
                <div className="results-header">
                  <span>Domain: {queryResults.domain}</span>
                  <span>Type: {queryResults.record_type}</span>
                  <span>Total Time: {queryResults.total_time_ms}ms</span>
                </div>
                <table className="results-table">
                  <thead>
                    <tr>
                      <th>Provider</th>
                      <th>Status</th>
                      <th>RCode</th>
                      <th>TTL</th>
                      <th>RTT</th>
                      <th>Answers</th>
                    </tr>
                  </thead>
                  <tbody>
                    {queryResults.answers.map((ans: any, i: number) => (
                      <tr key={i} className={ans.success ? 'success' : 'error'}>
                        <td>{ans.provider_name}</td>
                        <td>{ans.success ? 'OK' : 'Failed'}</td>
                        <td>{ans.rcode_name}</td>
                        <td>{ans.ttl}s</td>
                        <td>{ans.rtt_ms}ms</td>
                        <td>
                          {ans.answers && ans.answers.map((a: any, j: number) => (
                            <div key={j} className="answer">{a.data}</div>
                          ))}
                          {ans.error && <div className="error-msg">{ans.error}</div>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}

        {activeTab === 'providers' && (
          <div className="providers-panel">
            <div className="panel-header">
              <h2>DNS Providers</h2>
              <button className="btn-primary" onClick={handleNew}>Add Provider</button>
            </div>

            <table className="providers-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Protocol</th>
                  <th>Endpoint</th>
                  <th>Port</th>
                  <th>Timeout</th>
                  <th>Enabled</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {providers.map((p) => (
                  <tr key={p.id}>
                    <td>{p.name}</td>
                    <td>{p.protocol.toUpperCase()}</td>
                    <td className="endpoint">{p.endpoint}</td>
                    <td>{p.port}</td>
                    <td>{p.timeout}ms</td>
                    <td>{p.enabled ? 'Yes' : 'No'}</td>
                    <td className="actions">
                      <button
                        className="btn-small"
                        onClick={() => handleTest(p.id)}
                        disabled={testingId === p.id}
                      >
                        {testingId === p.id ? 'Testing...' : 'Test'}
                      </button>
                      <button className="btn-small" onClick={() => handleEdit(p)}>Edit</button>
                      <button className="btn-small btn-danger" onClick={() => handleDelete(p.id)}>Delete</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </main>

      {/* Provider Edit Modal */}
      {showModal && editingProvider && (
        <div className="modal-overlay" onClick={() => setShowModal(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h3>{editingProvider.id ? 'Edit Provider' : 'New Provider'}</h3>
            <div className="form-group">
              <label>Name</label>
              <input
                type="text"
                value={editingProvider.name}
                onChange={(e) => setEditingProvider({ ...editingProvider, name: e.target.value })}
              />
            </div>
            <div className="form-group">
              <label>Protocol</label>
              <select
                value={editingProvider.protocol}
                onChange={(e) => setEditingProvider({ ...editingProvider, protocol: e.target.value })}
              >
                <option value="doh">DoH (DNS over HTTPS)</option>
                <option value="dns">DNS (UDP)</option>
                <option value="dot">DoT (DNS over TLS)</option>
              </select>
            </div>
            <div className="form-group">
              <label>Endpoint (DoH URL / DoT Address)</label>
              <input
                type="text"
                value={editingProvider.endpoint}
                onChange={(e) => setEditingProvider({ ...editingProvider, endpoint: e.target.value })}
                placeholder="https://dns.example.com/dns-query"
              />
            </div>
            <div className="form-group">
              <label>Server Name (SNI for DoT)</label>
              <input
                type="text"
                value={editingProvider.server_name}
                onChange={(e) => setEditingProvider({ ...editingProvider, server_name: e.target.value })}
              />
            </div>
            <div className="form-row">
              <div className="form-group">
                <label>Port</label>
                <input
                  type="number"
                  value={editingProvider.port}
                  onChange={(e) => setEditingProvider({ ...editingProvider, port: parseInt(e.target.value) })}
                />
              </div>
              <div className="form-group">
                <label>Timeout (ms)</label>
                <input
                  type="number"
                  value={editingProvider.timeout}
                  onChange={(e) => setEditingProvider({ ...editingProvider, timeout: parseInt(e.target.value) })}
                />
              </div>
            </div>
            <div className="form-group checkbox">
              <label>
                <input
                  type="checkbox"
                  checked={editingProvider.enabled}
                  onChange={(e) => setEditingProvider({ ...editingProvider, enabled: e.target.checked })}
                />
                Enabled
              </label>
            </div>
            <div className="modal-actions">
              <button className="btn-secondary" onClick={() => setShowModal(false)}>Cancel</button>
              <button className="btn-primary" onClick={handleSave}>Save</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export default App;
