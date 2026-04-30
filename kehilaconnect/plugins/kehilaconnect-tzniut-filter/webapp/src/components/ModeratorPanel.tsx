import React, {useEffect, useState, useCallback} from 'react';

import './ModeratorPanel.css';

interface FlaggedItem {
    post_id: string;
    user_id: string;
    channel_id: string;
    reason: string;
    reviewed: boolean;
    approved: boolean;
}

const API_BASE = '/plugins/com.kehilaconnect.tzniut-filter';

/**
 * ModeratorPanel is the Right-Hand Sidebar component for the Tzniut Filter.
 * It shows a list of flagged posts awaiting moderator review and provides
 * approve/reject actions for each one.
 */
const ModeratorPanel: React.FC = () => {
    const [items, setItems] = useState<FlaggedItem[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [actionInFlight, setActionInFlight] = useState<string | null>(null);

    const fetchFlagged = useCallback(async () => {
        setError(null);
        try {
            const res = await fetch(`${API_BASE}/flagged`, {credentials: 'include'});
            if (!res.ok) {
                setError(`Server error ${res.status}`);
                return;
            }
            const data: FlaggedItem[] = await res.json();
            setItems(data ?? []);
        } catch {
            setError('Could not connect to the Tzniut Filter plugin.');
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        fetchFlagged();
        const timer = setInterval(fetchFlagged, 30_000); // refresh every 30s
        return () => clearInterval(timer);
    }, [fetchFlagged]);

    const review = useCallback(async (postId: string, approved: boolean) => {
        setActionInFlight(postId);
        try {
            await fetch(`${API_BASE}/review`, {
                method: 'POST',
                credentials: 'include',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({post_id: postId, approved}),
            });
            // Optimistically remove from list; next poll will reconcile.
            setItems((prev) => prev.filter((i) => i.post_id !== postId));
        } catch {
            setError('Action failed. Please try again.');
        } finally {
            setActionInFlight(null);
        }
    }, []);

    const pending = items.filter((i) => !i.reviewed);

    return (
        <div className='mod-panel'>
            <div className='mod-panel__header'>
                <span className='mod-panel__icon'>🛡️</span>
                <h3 className='mod-panel__title'>{'Content Moderation'}</h3>
                <button
                    className='mod-panel__refresh'
                    onClick={fetchFlagged}
                    title='Refresh'
                    aria-label='Refresh flagged items'
                >
                    {'↺'}
                </button>
            </div>

            {loading && <p className='mod-panel__status'>{'Loading…'}</p>}
            {error && <p className='mod-panel__error'>{error}</p>}

            {!loading && pending.length === 0 && !error && (
                <p className='mod-panel__empty'>
                    {'✅ No items pending review.'}
                </p>
            )}

            <ul className='mod-panel__list'>
                {pending.map((item) => (
                    <li key={item.post_id} className='mod-panel__item'>
                        <div className='mod-panel__item-meta'>
                            <span className='mod-panel__reason'>{item.reason}</span>
                            <span className='mod-panel__ids'>
                                {'Post: '}
                                <code>{item.post_id.slice(0, 8)}</code>
                                {' · User: '}
                                <code>{item.user_id.slice(0, 8)}</code>
                            </span>
                        </div>
                        <div className='mod-panel__actions'>
                            <button
                                className='mod-panel__btn mod-panel__btn--approve'
                                disabled={actionInFlight === item.post_id}
                                onClick={() => review(item.post_id, true)}
                            >
                                {'Approve'}
                            </button>
                            <button
                                className='mod-panel__btn mod-panel__btn--reject'
                                disabled={actionInFlight === item.post_id}
                                onClick={() => review(item.post_id, false)}
                            >
                                {'Remove'}
                            </button>
                        </div>
                    </li>
                ))}
            </ul>
        </div>
    );
};

export default ModeratorPanel;
