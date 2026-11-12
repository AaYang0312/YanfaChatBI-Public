import { MessageSquare, Database, LineChart } from 'lucide-react';
import { DefaultToastOptions } from 'react-hot-toast';
import { zhCN } from '../../i18n/zh-CN';

const WelcomeSection = ({ isSidebarExpanded, setShowConnectionModal }: { isSidebarExpanded: boolean, setShowConnectionModal: (show: boolean) => void, toastStyle: DefaultToastOptions }) => {
    return (
        <div className={`
            flex-1 
            flex 
            flex-col 
            items-center 
            justify-center
        p-8 
        mt-24
        md:mt-12
        min-h-[calc(100vh-4rem)] 
        transition-all 
        duration-300 
        ${isSidebarExpanded ? 'md:ml-80' : 'md:ml-20'}
    `}>
  <div className="w-full max-w-4xl mx-auto text-center mb-12">
    <h1 className="text-5xl font-bold mb-4">
     {zhCN.app.welcomeTitle}
    </h1>
    <p className="text-xl text-gray-600 mb-2 max-w-3xl mx-auto">
      {zhCN.app.welcomeSubtitle1}
      <br />
      {zhCN.app.welcomeSubtitle2}
    </p>
  </div>

  <div className="w-full max-w-4xl mx-auto grid md:grid-cols-3 gap-6 mb-12">
    <button
      className="
                    neo-border 
                    bg-white 
                    p-6 
                    rounded-lg
                    text-left
                    transition-all
                    duration-300
                    hover:-translate-y-1
                    hover:shadow-lg
                    hover:bg-[#FFDB58]/5
                    active:translate-y-0
                    disabled:opacity-50
                    disabled:cursor-not-allowed
                "
    >
      <div className="w-12 h-12 bg-[#FFDB58]/20 rounded-lg flex items-center justify-center mb-4">
        <MessageSquare className="w-6 h-6 text-black" />
      </div>
      <h3 className="text-lg font-bold mb-2">
       {zhCN.app.welcomeAskTitle}
      </h3>
      <p className="text-gray-600">
        {zhCN.app.welcomeAskBody}
      </p>
    </button>

    <button
      className="
                    neo-border 
                    bg-white 
                    p-6 
                    rounded-lg
                    text-left
                    transition-all
                    duration-300
                    hover:-translate-y-1
                    hover:shadow-lg
                    hover:bg-[#FFDB58]/5
                    active:translate-y-0
                "
    >
      <div className="w-12 h-12 bg-[#FFDB58]/20 rounded-lg flex items-center justify-center mb-4">
        <Database className="w-6 h-6 text-black" />
      </div>
      <h3 className="text-lg font-bold mb-2">
        {zhCN.app.welcomeMultiTitle}
      </h3>
      <p className="text-gray-600">
        {zhCN.app.welcomeMultiBody}
      </p>
    </button>

    <button
      className="
                    neo-border 
                    bg-white 
                    p-6 
                    rounded-lg
                    text-left
                    transition-all
                    duration-300
                    hover:-translate-y-1
                    hover:shadow-lg
                    hover:bg-[#FFDB58]/5
                    active:translate-y-0
                    disabled:opacity-50
                    disabled:cursor-not-allowed
                "
    >
      <div className="w-12 h-12 bg-[#FFDB58]/20 rounded-lg flex items-center justify-center mb-4">
        <LineChart className="w-6 h-6 text-black" />
      </div>
      <h3 className="text-lg font-bold mb-2">
        {zhCN.app.welcomeVizTitle}
      </h3>
      <p className="text-gray-600">
        {zhCN.app.welcomeVizBody}
      </p>
    </button>
  </div>

  <div className="text-center">
    <button
      onClick={() => setShowConnectionModal(true)}
      className="neo-button text-lg px-8 py-4 mb-4"
    >
      {zhCN.app.createConnection}
    </button>
    <p className="text-gray-600">
      {zhCN.app.orSelectExisting}
    </p>
  </div>
        </div>
    )
}

export default WelcomeSection;
