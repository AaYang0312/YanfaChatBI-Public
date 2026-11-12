export interface GoogleOAuthState {
    purpose: 'auth' | 'spreadsheet';
    action?: string; // Forward legacy callback actions so the server can reject unsupported requests.
}

export const parseGoogleOAuthCallback = (): { code: string; state: GoogleOAuthState } | null => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get('code');
    const stateParam = params.get('state');

    if (!code) {
        console.error('No authorization code in callback');
        return null;
    }

    // Try to parse state parameter
    let state: GoogleOAuthState = { purpose: 'auth' }; // Default to 'auth' purpose
    
    if (stateParam) {
        try {
            state = JSON.parse(atob(stateParam));
        } catch (error) {
            console.warn('Failed to parse OAuth state, using default:', error);
            // State parsing failed, use default
            state = { purpose: 'auth' };
        }
    } else {
        console.warn('No state parameter in callback, using default auth purpose');
    }

    return { code, state };
};
