import { useApp } from '../state/store';

export function Toast() {
  const { toast } = useApp();
  return (
    <div className="toast-wrap" aria-live="polite" aria-atomic="true">
      {toast && (
        <div className="toast" role="status" key={toast}>
          <span className="toast__spark" aria-hidden>
            ✦
          </span>
          {toast}
        </div>
      )}
    </div>
  );
}
