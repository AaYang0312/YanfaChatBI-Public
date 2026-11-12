import { LoginFormData, SignupFormData } from '../../types/auth';
import LoginForm from './LoginForm';

interface AuthFormProps {
  onLogin: (data: LoginFormData) => Promise<void>;
  /** Kept for call-site compatibility; signup, forgot and reset entries are removed. */
  onSignup?: (data: SignupFormData) => Promise<void>;
  startupError?: string | null;
}

// Registration and password recovery are administrator-managed deployment
// operations; the login screen exposes authentication only.
export default function AuthForm({ onLogin, startupError }: AuthFormProps) {
  return <LoginForm onLogin={onLogin} startupError={startupError} />;
}
