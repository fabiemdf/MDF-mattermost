import React, {useEffect, useState, useCallback} from 'react';

import './ZmanimPanel.css';

interface ShabbatStatus {
    shabbat: boolean;
    havdalah: string;
}

interface ZmanimRow {
    label: string;
    time: string;
}

/**
 * ZmanimPanel is registered as the Right-Hand Sidebar component.
 * It shows today's key zmanim (halachic times) for the user's configured
 * location and real-time Shabbat status.
 */
const ZmanimPanel: React.FC = () => {
    const [status, setStatus] = useState<ShabbatStatus | null>(null);
    const [loading, setLoading] = useState(true);

    const fetchStatus = useCallback(async () => {
        try {
            const res = await fetch(
                '/plugins/com.kehilaconnect.shabbat-mode/status',
                {credentials: 'include'},
            );
            if (res.ok) {
                const data: ShabbatStatus = await res.json();
                setStatus(data);
            }
        } catch {
            // ignore
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        fetchStatus();
        const timer = setInterval(fetchStatus, 5 * 60 * 1000);
        return () => clearInterval(timer);
    }, [fetchStatus]);

    const zmanim: ZmanimRow[] = status
        ? [
            {label: 'Candle Lighting', time: status.shabbat ? 'Already in Shabbat' : 'See HebCal for today\'s times'},
            {label: 'Havdalah', time: status.havdalah || '—'},
            {label: 'Status', time: status.shabbat ? '🕯 Shabbat / Yom Tov' : '✅ Weekday — Posting enabled'},
        ]
        : [];

    return (
        <div className='zmanim-panel'>
            <div className='zmanim-panel__header'>
                <span className='zmanim-panel__star'>✡</span>
                <h3 className='zmanim-panel__title'>{'Zmanim'}</h3>
            </div>

            {loading && (
                <p className='zmanim-panel__loading'>{'Loading zmanim…'}</p>
            )}

            {!loading && status && (
                <table className='zmanim-panel__table'>
                    <tbody>
                        {zmanim.map(({label, time}) => (
                            <tr key={label}>
                                <th className='zmanim-panel__label'>{label}</th>
                                <td className='zmanim-panel__time'>{time}</td>
                            </tr>
                        ))}
                    </tbody>
                </table>
            )}

            {!loading && !status && (
                <p className='zmanim-panel__error'>
                    {'Could not load zmanim. Check your network connection.'}
                </p>
            )}

            <p className='zmanim-panel__footnote'>
                {'Times are based on your configured location. '}
                <a
                    href='https://www.hebcal.com/shabbat/'
                    target='_blank'
                    rel='noopener noreferrer'
                    className='zmanim-panel__link'
                >
                    {'Full zmanim on HebCal →'}
                </a>
            </p>
        </div>
    );
};

export default ZmanimPanel;
