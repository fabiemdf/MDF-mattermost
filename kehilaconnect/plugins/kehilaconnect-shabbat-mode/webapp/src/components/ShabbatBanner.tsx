import React, {useEffect, useState, useCallback} from 'react';

import './ShabbatBanner.css';

interface ShabbatStatus {
    shabbat: boolean;
    havdalah: string;
}

const POLL_INTERVAL_MS = 5 * 60 * 1000; // re-check every 5 minutes

/**
 * ShabbatBanner renders a full-width banner above the channel list when
 * Shabbat or Yom Tov is active. It polls the plugin's /status endpoint so
 * the UI updates without requiring a page reload.
 */
const ShabbatBanner: React.FC = () => {
    const [status, setStatus] = useState<ShabbatStatus | null>(null);

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
            // Network error — keep showing last known state.
        }
    }, []);

    useEffect(() => {
        fetchStatus();
        const timer = setInterval(fetchStatus, POLL_INTERVAL_MS);
        return () => clearInterval(timer);
    }, [fetchStatus]);

    if (!status?.shabbat) {
        return null;
    }

    return (
        <div className='shabbat-banner' role='status' aria-live='polite'>
            <span className='shabbat-banner__icon'>✡</span>
            <span className='shabbat-banner__text'>
                {'Shabbat Shalom — KehilaConnect is in Shabbat Mode.'}
                {status.havdalah && (
                    <span className='shabbat-banner__time'>
                        {` Havdalah at ${status.havdalah}`}
                    </span>
                )}
            </span>
        </div>
    );
};

export default ShabbatBanner;
