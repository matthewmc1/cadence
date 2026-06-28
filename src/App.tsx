import { useEffect } from 'react';
import './styles/shell.css';
import { StoreProvider, useApp, useActions } from './state/store';
import { AppHeader } from './components/AppHeader';
import { Toast } from './components/Toast';
import { TaskEditor } from './components/TaskEditor';
import { LoginView } from './views/LoginView';
import { TodayView } from './views/TodayView';
import { InsightsView } from './views/InsightsView';
import { PlanView } from './views/PlanView';
import { BoardView } from './views/BoardView';

function Stage() {
  const { view, status, error, toast, highlightTaskId } = useApp();
  const { clearToast, clearHighlight, reload } = useActions();

  useEffect(() => {
    if (!toast) return;
    const id = window.setTimeout(clearToast, 3600);
    return () => window.clearTimeout(id);
  }, [toast, clearToast]);

  useEffect(() => {
    if (!highlightTaskId) return;
    const id = window.setTimeout(clearHighlight, 2200);
    return () => window.clearTimeout(id);
  }, [highlightTaskId, clearHighlight]);

  return (
    <main className="stage" data-view={view}>
      {(status === 'loading' || status === 'idle') && (
        <div className="stage__state">
          <span className="wordmark__name" style={{ fontSize: 26 }}>
            Cadence
          </span>
          <p className="stage__state-msg">Arranging your day…</p>
        </div>
      )}
      {status === 'error' && (
        <div className="stage__state">
          <p className="stage__state-msg">Couldn't reach the Cadence server.</p>
          <p className="stage__state-sub">{error}</p>
          <button className="btn-primary" onClick={reload} style={{ marginTop: 18 }}>
            Try again
          </button>
        </div>
      )}
      {status === 'ready' && (
        <div className="stage__inner" key={view}>
          {view === 'today' && <TodayView />}
          {view === 'insights' && <InsightsView />}
          {view === 'plan' && <PlanView />}
          {view === 'board' && <BoardView />}
        </div>
      )}
      <Toast />
    </main>
  );
}

function Shell() {
  return (
    <div className="app">
      <AppHeader />
      <Stage />
      <TaskEditor />
    </div>
  );
}

function Root() {
  const { authStatus } = useApp();
  if (authStatus === 'checking') {
    return (
      <div className="app">
        <main className="stage">
          <div className="stage__state">
            <span className="wordmark__name" style={{ fontSize: 26 }}>
              Cadence
            </span>
          </div>
        </main>
      </div>
    );
  }
  if (authStatus === 'anon') return <LoginView />;
  return <Shell />;
}

export function App() {
  return (
    <StoreProvider>
      <Root />
    </StoreProvider>
  );
}
